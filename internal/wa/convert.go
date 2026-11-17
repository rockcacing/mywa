package wa

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Content is the part of a WhatsApp message mywa stores: a type, its text (or caption)
// and the id of the message it quotes, if any.
type Content struct {
	Type     string
	Text     string
	QuotedID string
}

// Message types stored in messages.type.
const (
	TypeText     = "text"
	TypeImage    = "image"
	TypeVideo    = "video"
	TypeGIF      = "gif"
	TypeAudio    = "audio"
	TypeVoice    = "voice"
	TypeDocument = "document"
	TypeSticker  = "sticker"
	TypeLocation = "location"
	TypeContact  = "contact"
	TypePoll     = "poll"
	TypeEvent    = "event"
	TypeUnknown  = "unknown"
)

// Extract maps an (already unwrapped) WhatsApp message to Content.
// ok is false for messages that are not chat content: protocol messages (edits,
// revokes, history notifications), reactions and bare key-distribution messages.
func Extract(m *waE2E.Message) (c Content, ok bool) {
	if m == nil {
		return c, false
	}
	switch {
	case m.GetProtocolMessage() != nil, m.GetReactionMessage() != nil:
		return c, false
	case m.GetConversation() != "":
		return Content{Type: TypeText, Text: m.GetConversation()}, true
	case m.GetExtendedTextMessage() != nil:
		x := m.GetExtendedTextMessage()
		return Content{Type: TypeText, Text: x.GetText(), QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetImageMessage() != nil:
		x := m.GetImageMessage()
		return Content{Type: TypeImage, Text: x.GetCaption(), QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetVideoMessage() != nil:
		x := m.GetVideoMessage()
		t := TypeVideo
		if x.GetGifPlayback() {
			t = TypeGIF
		}
		return Content{Type: t, Text: x.GetCaption(), QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetPtvMessage() != nil:
		x := m.GetPtvMessage()
		return Content{Type: TypeVideo, Text: x.GetCaption(), QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetAudioMessage() != nil:
		x := m.GetAudioMessage()
		t := TypeAudio
		if x.GetPTT() {
			t = TypeVoice
		}
		return Content{Type: t, QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetDocumentMessage() != nil:
		x := m.GetDocumentMessage()
		text := x.GetCaption()
		if text == "" {
			text = firstNonEmpty(x.GetFileName(), x.GetTitle())
		}
		return Content{Type: TypeDocument, Text: text, QuotedID: x.GetContextInfo().GetStanzaID()}, true
	case m.GetStickerMessage() != nil:
		return Content{Type: TypeSticker, QuotedID: m.GetStickerMessage().GetContextInfo().GetStanzaID()}, true
	case m.GetLocationMessage() != nil:
		x := m.GetLocationMessage()
		return Content{Type: TypeLocation, Text: firstNonEmpty(x.GetName(), x.GetAddress())}, true
	case m.GetLiveLocationMessage() != nil:
		return Content{Type: TypeLocation, Text: m.GetLiveLocationMessage().GetCaption()}, true
	case m.GetContactMessage() != nil:
		return Content{Type: TypeContact, Text: m.GetContactMessage().GetDisplayName()}, true
	case m.GetContactsArrayMessage() != nil:
		return Content{Type: TypeContact, Text: m.GetContactsArrayMessage().GetDisplayName()}, true
	case pollName(m) != nil:
		return Content{Type: TypePoll, Text: *pollName(m)}, true
	case m.GetEventMessage() != nil:
		return Content{Type: TypeEvent, Text: m.GetEventMessage().GetName()}, true
	case !hasContent(m):
		// Key distribution and context-only frames carry no content of their own.
		return c, false
	default:
		return Content{Type: TypeUnknown}, true
	}
}

func pollName(m *waE2E.Message) *string {
	for _, p := range []*waE2E.PollCreationMessage{
		m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6(),
	} {
		if p != nil {
			n := p.GetName()
			return &n
		}
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// hasContent reports whether m has any populated field besides the metadata-only
// senderKeyDistributionMessage and messageContextInfo.
func hasContent(m *waE2E.Message) bool {
	found := false
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		switch fd.Name() {
		case "senderKeyDistributionMessage", "messageContextInfo":
			return true
		}
		found = true
		return false
	})
	return found
}
