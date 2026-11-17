package wa

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestExtract(t *testing.T) {
	s := proto.String
	tests := []struct {
		name string
		msg  *waE2E.Message
		want Content
		ok   bool
	}{
		{"conversation", &waE2E.Message{Conversation: s("hi")}, Content{Type: TypeText, Text: "hi"}, true},
		{"reply", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: s("yes"), ContextInfo: &waE2E.ContextInfo{StanzaID: s("Q1")}}},
			Content{Type: TypeText, Text: "yes", QuotedID: "Q1"}, true},
		{"image caption", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: s("look")}},
			Content{Type: TypeImage, Text: "look"}, true},
		{"gif", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{GifPlayback: proto.Bool(true)}},
			Content{Type: TypeGIF}, true},
		{"voice note", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true)}},
			Content{Type: TypeVoice}, true},
		{"document name", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: s("a.pdf")}},
			Content{Type: TypeDocument, Text: "a.pdf"}, true},
		{"location", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{Name: s("Office")}},
			Content{Type: TypeLocation, Text: "Office"}, true},
		{"poll v3", &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: s("Lunch?")}},
			Content{Type: TypePoll, Text: "Lunch?"}, true},
		{"protocol", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}, Content{}, false},
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: s("👍")}}, Content{}, false},
		{"key distribution only", &waE2E.Message{
			SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{},
			MessageContextInfo:           &waE2E.MessageContextInfo{}}, Content{}, false},
		{"unknown kind", &waE2E.Message{
			ButtonsMessage:     &waE2E.ButtonsMessage{},
			MessageContextInfo: &waE2E.MessageContextInfo{}}, Content{Type: TypeUnknown}, true},
		{"nil", nil, Content{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Extract(tt.msg)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("Extract = %+v, %v; want %+v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseChatID(t *testing.T) {
	good := map[string]types.JID{
		"6281234567890":                types.NewJID("6281234567890", types.DefaultUserServer),
		"+62 812-3456-7890":            types.NewJID("6281234567890", types.DefaultUserServer),
		"6281234567890@s.whatsapp.net": types.NewJID("6281234567890", types.DefaultUserServer),
		"120363000000000000@g.us":      types.NewJID("120363000000000000", types.GroupServer),
		"123456789@lid":                types.NewJID("123456789", types.HiddenUserServer),
	}
	for in, want := range good {
		got, err := ParseChatID(in)
		if err != nil || got != want {
			t.Errorf("ParseChatID(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "123", "status@broadcast", "1@newsletter", "62812abc"} {
		if _, err := ParseChatID(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseChatID(%q) error = %v, want ErrInvalid", in, err)
		}
	}
}

func TestMediaPart(t *testing.T) {
	in := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: proto.String("image/jpeg"), Width: proto.Uint32(800), Height: proto.Uint32(600),
		JPEGThumbnail: []byte{1, 2}, DirectPath: proto.String("/v/x"), MediaKey: []byte{9},
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q")},
	}}
	part, info := mediaPart(in)
	if part.GetImageMessage().GetContextInfo() != nil {
		t.Fatal("context info not stripped")
	}
	if in.GetImageMessage().GetContextInfo() == nil {
		t.Fatal("original message modified")
	}
	if info.Width != 800 || info.Height != 600 || !info.HasThumb || info.Mimetype != "image/jpeg" {
		t.Fatalf("info = %+v", info)
	}
	d, thumb, mime, _, _ := downloadable(part)
	if d == nil || d.GetDirectPath() != "/v/x" || len(thumb) != 2 || mime != "image/jpeg" {
		t.Fatal("downloadable did not round-trip")
	}
	if p, i := mediaPart(&waE2E.Message{Conversation: proto.String("hi")}); p != nil || i != nil {
		t.Fatal("text message reported media")
	}
	for mt, ok := range map[string]bool{"image/jpeg": true, "video/mp4": true, "audio/ogg; codecs=opus": true,
		"image/svg+xml": false, "text/html": false, "application/pdf": false} {
		if SafeInlineType(mt) != ok {
			t.Errorf("SafeInlineType(%q) = %v", mt, !ok)
		}
	}
}

func TestClassify(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	jpg := append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 32)...)
	mp4 := append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 32)...)
	pdf := []byte("%PDF-1.7 ...")
	cases := []struct {
		name string
		req  SendMediaRequest
		kind sendKind
		mime string
	}{
		{"jpeg", SendMediaRequest{Data: jpg, FileName: "a.jpg"}, kindImage, "image/jpeg"},
		{"png with wrong client type", SendMediaRequest{Data: png, Mimetype: "application/octet-stream"}, kindImage, "image/png"},
		{"photo as document", SendMediaRequest{Data: jpg, FileName: "a.jpg", AsDocument: true}, kindDocument, "image/jpeg"},
		{"mp4", SendMediaRequest{Data: mp4, FileName: "v.mp4"}, kindVideo, "video/mp4"},
		{"pdf", SendMediaRequest{Data: pdf, FileName: "x.pdf"}, kindDocument, "application/pdf"},
		{"gif stays a file", SendMediaRequest{Data: []byte("GIF89a......"), FileName: "a.gif"}, kindDocument, "image/gif"},
		{"mp3", SendMediaRequest{Data: []byte("ID3......"), FileName: "s.mp3", Mimetype: "audio/mpeg"}, kindAudio, "audio/mpeg"},
		{"html is never inline", SendMediaRequest{Data: []byte("<html><script>"), FileName: "x.html"}, kindDocument, "text/html"},
	}
	for _, c := range cases {
		k, mt := classify(c.req)
		if k != c.kind || mt != c.mime {
			t.Errorf("%s: classify = %v %q, want %v %q", c.name, k, mt, c.kind, c.mime)
		}
	}
}

func TestImageInfoThumbnail(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	w, h, thumb := imageInfo(buf.Bytes())
	if w != 400 || h != 200 || len(thumb) == 0 {
		t.Fatalf("imageInfo = %d×%d, %d-byte thumb", w, h, len(thumb))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumb))
	if err != nil || cfg.Width != 72 || cfg.Height != 36 {
		t.Fatalf("thumb = %+v, %v", cfg, err)
	}
}
