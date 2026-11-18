package wa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	mystore "github.com/rockcacing/mywa/internal/store"
)

// Media errors.
var (
	ErrNoMedia      = errors.New("message has no downloadable media")
	ErrMediaExpired = errors.New("media is no longer available on WhatsApp's servers")
	ErrTooLarge     = errors.New("media is too large to download through mywa")
)

// MaxMediaBytes caps a single download; whatsmeow holds the whole file in memory.
const MaxMediaBytes = 100 << 20

// Media is a downloaded attachment (or its thumbnail).
type Media struct {
	Data     []byte
	Mimetype string
	FileName string
}

// mediaPart returns a copy of m holding only its downloadable attachment (without the
// quoted-message context, which can be large), plus the metadata clients need.
func mediaPart(m *waE2E.Message) (*waE2E.Message, *mystore.MediaInfo) {
	if m == nil {
		return nil, nil
	}
	switch {
	case m.GetImageMessage() != nil:
		x := proto.Clone(m.GetImageMessage()).(*waE2E.ImageMessage)
		x.ContextInfo = nil
		return &waE2E.Message{ImageMessage: x}, &mystore.MediaInfo{
			Mimetype: x.GetMimetype(), Size: x.GetFileLength(), Width: x.GetWidth(), Height: x.GetHeight(),
			HasThumb: len(x.GetJPEGThumbnail()) > 0,
		}
	case m.GetVideoMessage() != nil || m.GetPtvMessage() != nil:
		src := m.GetVideoMessage()
		if src == nil {
			src = m.GetPtvMessage()
		}
		x := proto.Clone(src).(*waE2E.VideoMessage)
		x.ContextInfo = nil
		return &waE2E.Message{VideoMessage: x}, &mystore.MediaInfo{
			Mimetype: x.GetMimetype(), Size: x.GetFileLength(), Width: x.GetWidth(), Height: x.GetHeight(),
			Seconds: x.GetSeconds(), HasThumb: len(x.GetJPEGThumbnail()) > 0,
		}
	case m.GetAudioMessage() != nil:
		x := proto.Clone(m.GetAudioMessage()).(*waE2E.AudioMessage)
		x.ContextInfo = nil
		return &waE2E.Message{AudioMessage: x}, &mystore.MediaInfo{
			Mimetype: x.GetMimetype(), Size: x.GetFileLength(), Seconds: x.GetSeconds(),
		}
	case m.GetDocumentMessage() != nil:
		x := proto.Clone(m.GetDocumentMessage()).(*waE2E.DocumentMessage)
		x.ContextInfo = nil
		return &waE2E.Message{DocumentMessage: x}, &mystore.MediaInfo{
			Mimetype: x.GetMimetype(), Size: x.GetFileLength(), FileName: firstNonEmpty(x.GetFileName(), x.GetTitle()),
			HasThumb: len(x.GetJPEGThumbnail()) > 0,
		}
	case m.GetStickerMessage() != nil:
		x := proto.Clone(m.GetStickerMessage()).(*waE2E.StickerMessage)
		x.ContextInfo = nil
		return &waE2E.Message{StickerMessage: x}, &mystore.MediaInfo{
			Mimetype: x.GetMimetype(), Size: x.GetFileLength(), Width: x.GetWidth(), Height: x.GetHeight(),
		}
	}
	return nil, nil
}

// downloadable picks the attachment out of a stored media part.
func downloadable(m *waE2E.Message) (d whatsmeow.DownloadableMessage, thumb []byte, mime, name string, size uint64) {
	switch {
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return x, x.GetJPEGThumbnail(), x.GetMimetype(), "", x.GetFileLength()
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		return x, x.GetJPEGThumbnail(), x.GetMimetype(), "", x.GetFileLength()
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		return x, nil, x.GetMimetype(), "", x.GetFileLength()
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		return x, x.GetJPEGThumbnail(), x.GetMimetype(), firstNonEmpty(x.GetFileName(), x.GetTitle()), x.GetFileLength()
	case m.GetStickerMessage() != nil:
		x := m.GetStickerMessage()
		return x, nil, x.GetMimetype(), "", x.GetFileLength()
	}
	return nil, nil, "", "", 0
}

// Media returns a message's attachment, or its embedded thumbnail when thumb is set.
// Full files are fetched from WhatsApp once and then served from the disk cache.
func (s *Session) Media(ctx context.Context, chatID, msgID string, thumb bool) (Media, error) {
	jid, err := ParseChatID(chatID)
	if err != nil {
		return Media{}, err
	}
	chat := s.canonicalChat(ctx, jid, types.EmptyJID)
	blob, err := s.m.db.GetMediaBlob(ctx, s.id, chat.String(), msgID)
	if errors.Is(err, mystore.ErrNotFound) {
		return Media{}, ErrNoMedia
	} else if err != nil {
		return Media{}, err
	}
	var part waE2E.Message
	if err := proto.Unmarshal(blob, &part); err != nil {
		return Media{}, fmt.Errorf("decode stored media: %w", err)
	}
	d, th, mime, name, size := downloadable(&part)
	if d == nil {
		return Media{}, ErrNoMedia
	}
	if thumb {
		if len(th) == 0 {
			return Media{}, ErrNoMedia
		}
		return Media{Data: th, Mimetype: "image/jpeg"}, nil
	}
	if size > MaxMediaBytes {
		return Media{}, ErrTooLarge
	}

	path := s.m.mediaPath(s.id, chat.String(), msgID)
	if data, err := os.ReadFile(path); err == nil {
		return Media{Data: data, Mimetype: mime, FileName: name}, nil
	}
	cli, err := s.readyClient()
	if err != nil {
		return Media{}, err
	}
	data, err := cli.Download(ctx, d)
	switch {
	case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404), errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410),
		errors.Is(err, whatsmeow.ErrNoURLPresent):
		return Media{}, ErrMediaExpired
	case err != nil:
		return Media{}, fmt.Errorf("%w: download: %v", ErrUpstream, err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		s.log.Warn("cache media", "err", err)
	}
	return Media{Data: data, Mimetype: mime, FileName: name}, nil
}

// mediaPath is the cache file for one message's attachment. Names are hashed so chat
// and message ids never become path components.
func (m *Manager) mediaPath(sessionID, chat, msgID string) string {
	sum := sha256.Sum256([]byte(chat + "\x00" + msgID))
	return filepath.Join(m.mediaDir, sessionID, hex.EncodeToString(sum[:]))
}

// removeMediaCache deletes a session's cached media.
func (m *Manager) removeMediaCache(sessionID string) {
	if m.mediaDir == "" || !sessionIDRe.MatchString(sessionID) {
		return
	}
	if err := os.RemoveAll(filepath.Join(m.mediaDir, sessionID)); err != nil {
		m.log.Warn("remove media cache", "session", sessionID, "err", err)
	}
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SafeInlineType reports whether a media type can be shown inline by browsers without
// running script (images other than SVG, audio, video).
func SafeInlineType(mime string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch base {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}
	return strings.HasPrefix(base, "video/") || strings.HasPrefix(base, "audio/")
}

// ErrReloadTimeout means the phone did not re-send the message in time.
var ErrReloadTimeout = errors.New("your phone didn't send the message again in time; make sure it is online and try again")

// reloadWait is how long ReloadMedia waits for the phone's answer.
var reloadWait = 30 * time.Second

// ReloadMedia makes an older media message downloadable again. Messages stored before
// mywa kept download details can't be fetched; this asks the phone to re-send the 50
// messages up to and including this one (an on-demand history sync), which fills in
// the details for every attachment in that range. It waits for the answer and returns
// the updated message.
func (s *Session) ReloadMedia(ctx context.Context, chatID, msgID string) (mystore.Message, error) {
	jid, err := ParseChatID(chatID)
	if err != nil {
		return mystore.Message{}, err
	}
	chat := s.canonicalChat(ctx, jid, types.EmptyJID)
	m, err := s.m.db.GetMessage(ctx, s.id, chat.String(), msgID)
	if errors.Is(err, mystore.ErrNotFound) {
		return m, fmt.Errorf("%w: message %q not found in this chat", ErrInvalid, msgID)
	} else if err != nil {
		return m, err
	}
	if m.Media != nil {
		return m, nil // already downloadable
	}
	switch m.Type {
	case TypeImage, TypeVideo, TypeGIF, TypeAudio, TypeVoice, TypeDocument, TypeSticker:
	default:
		return m, fmt.Errorf("%w: this message has no attachment", ErrInvalid)
	}
	cli, err := s.readyClient()
	if err != nil {
		return m, err
	}
	// The phone answers with the messages *before* the one we name, so name the next one.
	next, err := s.m.db.NextMessage(ctx, s.id, chat.String(), msgID)
	if errors.Is(err, mystore.ErrNotFound) {
		return m, fmt.Errorf("%w: this is the newest message in the chat; it can be reloaded once another message arrives", ErrInvalid)
	} else if err != nil {
		return m, err
	}
	anchor := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: next.FromMe, IsGroup: chat.Server == types.GroupServer},
		ID:            next.ID,
		Timestamp:     time.Unix(next.Timestamp, 0),
	}
	if _, err := cli.SendPeerMessage(ctx, cli.BuildHistorySyncRequest(anchor, 50)); err != nil {
		return m, fmt.Errorf("%w: ask phone: %v", ErrUpstream, err)
	}
	s.log.Info("requested message history for media reload", "chat", chat.String(), "before", next.ID)

	deadline := time.NewTimer(reloadWait)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return m, ctx.Err()
		case <-deadline.C:
			return m, ErrReloadTimeout
		case <-tick.C:
			if got, err := s.m.db.GetMessage(ctx, s.id, chat.String(), msgID); err == nil && got.Media != nil {
				return got, nil
			}
		}
	}
}

// ReloadMedia asks the phone to re-send an older media message; see Session.ReloadMedia.
func (m *Manager) ReloadMedia(ctx context.Context, id, chatID, msgID string) (mystore.Message, error) {
	s, err := m.get(id)
	if err != nil {
		return mystore.Message{}, err
	}
	return s.ReloadMedia(ctx, chatID, msgID)
}
