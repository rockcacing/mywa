package wa

import (
	"errors"
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
