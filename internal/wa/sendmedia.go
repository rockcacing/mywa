package wa

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG for DecodeConfig/Decode
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/rockcacing/mywa/internal/hub"
	mystore "github.com/rockcacing/mywa/internal/store"
)

// SendMediaRequest is an attachment to send. Mimetype and FileName come from the client
// and are only hints: the content is sniffed to decide how WhatsApp should show it.
type SendMediaRequest struct {
	ChatID     string
	Caption    string
	FileName   string
	Mimetype   string
	Data       []byte
	AsDocument bool // always send as a file, even if it is a photo or video
}

type sendKind int

const (
	kindDocument sendKind = iota
	kindImage
	kindVideo
	kindAudio
)

// classify picks the WhatsApp message kind and the mimetype to declare. WhatsApp shows
// only JPEG/PNG as photos, MP4/3GP as videos and common audio formats as audio; anything
// else (GIF, WebP, HEIC, …) is safest as a document.
func classify(req SendMediaRequest) (sendKind, string) {
	sniffed := http.DetectContentType(req.Data)
	mt := baseType(req.Mimetype)
	if mt == "" || mt == "application/octet-stream" {
		mt = baseType(mime.TypeByExtension(strings.ToLower(filepath.Ext(req.FileName))))
	}
	if mt == "" || mt == "application/octet-stream" {
		mt = baseType(sniffed)
	}
	if req.AsDocument {
		return kindDocument, orDefault(mt, "application/octet-stream")
	}
	switch sb := baseType(sniffed); {
	case sb == "image/jpeg" || sb == "image/png":
		return kindImage, sb
	case sb == "video/mp4" || mt == "video/mp4" || mt == "video/3gpp":
		if strings.HasPrefix(mt, "video/") {
			return kindVideo, mt
		}
		return kindVideo, "video/mp4"
	case strings.HasPrefix(mt, "audio/") || sb == "application/ogg" || strings.HasPrefix(sb, "audio/"):
		switch {
		case mt == "audio/ogg" || sb == "application/ogg":
			return kindAudio, "audio/ogg; codecs=opus"
		case strings.HasPrefix(mt, "audio/"):
			return kindAudio, mt
		default:
			return kindAudio, sb
		}
	}
	return kindDocument, orDefault(mt, "application/octet-stream")
}

func baseType(t string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(t, ";", 2)[0]))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// imageInfo returns the size of a JPEG/PNG and a small JPEG preview for the chat bubble.
func imageInfo(data []byte) (w, h uint32, thumb []byte) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil
	}
	b := img.Bounds()
	w, h = uint32(b.Dx()), uint32(b.Dy())
	const max = 72
	tw, th := b.Dx(), b.Dy()
	if tw >= th && tw > max {
		th, tw = th*max/tw, max
	} else if th > tw && th > max {
		tw, th = tw*max/th, max
	}
	tw, th = maxInt(tw, 1), maxInt(th, 1)
	small := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ { // nearest neighbour is plenty for a 72px preview
		for x := 0; x < tw; x++ {
			small.Set(x, y, img.At(b.Min.X+x*b.Dx()/tw, b.Min.Y+y*b.Dy()/th))
		}
	}
	var buf bytes.Buffer
	if jpeg.Encode(&buf, small, &jpeg.Options{Quality: 60}) == nil {
		thumb = buf.Bytes()
	}
	return w, h, thumb
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SendMedia uploads an attachment and sends it, with an optional caption.
func (s *Session) SendMedia(ctx context.Context, req SendMediaRequest) (mystore.Message, error) {
	if len(req.Data) == 0 {
		return mystore.Message{}, fmt.Errorf("%w: file is empty", ErrInvalid)
	}
	if len(req.Data) > MaxMediaBytes {
		return mystore.Message{}, ErrTooLarge
	}
	if utf8.RuneCountInString(req.Caption) > maxTextLen {
		return mystore.Message{}, fmt.Errorf("%w: caption is longer than %d characters", ErrInvalid, maxTextLen)
	}
	to, err := ParseChatID(req.ChatID)
	if err != nil {
		return mystore.Message{}, err
	}
	cli, err := s.readyClient()
	if err != nil {
		return mystore.Message{}, err
	}
	chat := s.canonicalChat(ctx, to, types.EmptyJID)

	kind, mt := classify(req)
	if kind == kindAudio && strings.TrimSpace(req.Caption) != "" {
		kind = kindDocument // WhatsApp audio messages have no caption; keep it by sending a file
	}
	name := filepath.Base(strings.ReplaceAll(req.FileName, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	appInfo := map[sendKind]whatsmeow.MediaType{
		kindImage: whatsmeow.MediaImage, kindVideo: whatsmeow.MediaVideo,
		kindAudio: whatsmeow.MediaAudio, kindDocument: whatsmeow.MediaDocument,
	}[kind]
	up, err := cli.Upload(ctx, req.Data, appInfo)
	if err != nil {
		return mystore.Message{}, fmt.Errorf("%w: upload: %v", ErrUpstream, err)
	}
	var caption *string
	if strings.TrimSpace(req.Caption) != "" {
		caption = proto.String(req.Caption)
	}
	msg := &waE2E.Message{}
	switch kind {
	case kindImage:
		w, h, thumb := imageInfo(req.Data)
		msg.ImageMessage = &waE2E.ImageMessage{
			Caption: caption, Mimetype: proto.String(mt), Width: proto.Uint32(w), Height: proto.Uint32(h),
			JPEGThumbnail: thumb, URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength),
		}
	case kindVideo:
		msg.VideoMessage = &waE2E.VideoMessage{
			Caption: caption, Mimetype: proto.String(mt),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength),
		}
	case kindAudio:
		msg.AudioMessage = &waE2E.AudioMessage{
			Mimetype: proto.String(mt),
			URL:      proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength),
		}
	default:
		msg.DocumentMessage = &waE2E.DocumentMessage{
			Caption: caption, Mimetype: proto.String(mt), FileName: proto.String(name), Title: proto.String(name),
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength),
		}
	}

	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return mystore.Message{}, fmt.Errorf("%w: send: %v", ErrUpstream, err)
	}
	content, _ := Extract(msg)
	info := s.Info()
	m := mystore.Message{
		ID: resp.ID, ChatID: chat.String(), SenderID: info.JID, SenderName: info.PushName,
		FromMe: true, Timestamp: resp.Timestamp.Unix(), Type: content.Type, Text: content.Text,
		Status: mystore.StatusSent,
	}
	if m.Timestamp <= 0 {
		m.Timestamp = nowUnix()
	}
	if part, mi := mediaPart(msg); part != nil {
		if blob, err := proto.Marshal(part); err == nil {
			m.MediaBlob, m.Media = blob, mi
		}
	}
	if _, err := s.m.db.SaveMessage(ctx, s.id, m, false); err != nil {
		s.log.Error("store sent media", "err", err)
	}
	// We already have the file: cache it so viewing our own message needs no download.
	if err := writeFileAtomic(s.m.mediaPath(s.id, chat.String(), m.ID), req.Data); err != nil {
		s.log.Warn("cache sent media", "err", err)
	}
	s.ensureChatName(ctx, cli, chat, "")
	s.m.hub.Publish(s.id, hub.MessageSent, m)
	return m, nil
}

// SendMedia sends an attachment from one of the manager's sessions.
func (m *Manager) SendMedia(ctx context.Context, id string, req SendMediaRequest) (mystore.Message, error) {
	s, err := m.get(id)
	if err != nil {
		return mystore.Message{}, err
	}
	return s.SendMedia(ctx, req)
}
