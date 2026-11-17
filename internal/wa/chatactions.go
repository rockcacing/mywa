package wa

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/rockcacing/mywa/internal/hub"
	mystore "github.com/rockcacing/mywa/internal/store"
)

// MaxPinnedChats is WhatsApp's limit on pinned chats.
const MaxPinnedChats = 3

// ChatFlags is the result of an archive or pin action.
type ChatFlags struct {
	ChatID   string `json:"chatId"`
	Archived bool   `json:"archived"`
	Pinned   bool   `json:"pinned"`
}

func (s *Session) chatForAction(ctx context.Context, chatID string) (types.JID, mystore.ChatState, error) {
	jid, err := ParseChatID(chatID)
	if err != nil {
		return jid, mystore.ChatState{}, err
	}
	chat := s.canonicalChat(ctx, jid, types.EmptyJID)
	st, err := s.m.db.GetChatState(ctx, s.id, chat.String())
	if errors.Is(err, mystore.ErrNotFound) {
		return chat, st, fmt.Errorf("%w: chat %q not found", ErrInvalid, chatID)
	}
	return chat, st, err
}

// SetArchived archives or unarchives a chat on the account (all linked devices).
// Archiving also unpins the chat, as WhatsApp does.
func (s *Session) SetArchived(ctx context.Context, chatID string, archive bool) (ChatFlags, error) {
	chat, st, err := s.chatForAction(ctx, chatID)
	if err != nil {
		return ChatFlags{}, err
	}
	cli, err := s.readyClient()
	if err != nil {
		return ChatFlags{}, err
	}
	// WhatsApp records which message the chat was archived at; a newer one unarchives it
	// again when the account doesn't keep chats archived.
	var lastTS time.Time
	var lastKey *waCommon.MessageKey
	if archive && st.LastMessageID != "" {
		lastTS = time.Unix(st.LastMessageTS, 0)
		if m, err := s.m.db.GetMessage(ctx, s.id, chat.String(), st.LastMessageID); err == nil {
			lastKey = &waCommon.MessageKey{
				RemoteJID: proto.String(chat.String()),
				FromMe:    proto.Bool(m.FromMe),
				ID:        proto.String(m.ID),
			}
			if chat.Server == types.GroupServer && !m.FromMe && m.SenderID != "" {
				lastKey.Participant = proto.String(m.SenderID)
			}
		}
	}
	if err := cli.SendAppState(ctx, appstate.BuildArchive(chat, archive, lastTS, lastKey)); err != nil {
		return ChatFlags{}, fmt.Errorf("%w: archive: %v", ErrUpstream, err)
	}
	archTS := st.LastMessageTS
	if archTS <= 0 {
		archTS = nowUnix()
	}
	if err := s.m.db.SetArchived(ctx, s.id, chat.String(), archive, archTS); err != nil {
		return ChatFlags{}, err
	}
	pinned := st.Pinned
	if archive && pinned {
		if err := s.m.db.SetPinned(ctx, s.id, chat.String(), 0); err != nil {
			return ChatFlags{}, err
		}
		pinned = false
	}
	res := ChatFlags{ChatID: chat.String(), Archived: archive, Pinned: pinned}
	s.m.hub.Publish(s.id, hub.ChatUpdated, map[string]any{"chatId": res.ChatID, "archived": archive})
	return res, nil
}

// SetPinned pins or unpins a chat on the account.
func (s *Session) SetPinned(ctx context.Context, chatID string, pin bool) (ChatFlags, error) {
	chat, st, err := s.chatForAction(ctx, chatID)
	if err != nil {
		return ChatFlags{}, err
	}
	if pin && !st.Pinned {
		if st.Archived {
			return ChatFlags{}, fmt.Errorf("%w: archived chats can't be pinned; unarchive it first", ErrInvalid)
		}
		n, err := s.m.db.PinnedCount(ctx, s.id)
		if err != nil {
			return ChatFlags{}, err
		}
		if n >= MaxPinnedChats {
			return ChatFlags{}, fmt.Errorf("%w: you can only pin up to %d chats", ErrInvalid, MaxPinnedChats)
		}
	}
	cli, err := s.readyClient()
	if err != nil {
		return ChatFlags{}, err
	}
	if err := cli.SendAppState(ctx, appstate.BuildPin(chat, pin)); err != nil {
		return ChatFlags{}, fmt.Errorf("%w: pin: %v", ErrUpstream, err)
	}
	var pinnedAt int64
	if pin {
		pinnedAt = nowUnix()
	}
	if err := s.m.db.SetPinned(ctx, s.id, chat.String(), pinnedAt); err != nil {
		return ChatFlags{}, err
	}
	res := ChatFlags{ChatID: chat.String(), Archived: st.Archived, Pinned: pin}
	s.m.hub.Publish(s.id, hub.ChatUpdated, map[string]any{"chatId": res.ChatID, "pinned": pin})
	return res, nil
}

// SetChatArchived archives or unarchives one of the session's chats.
func (m *Manager) SetChatArchived(ctx context.Context, id, chatID string, archive bool) (ChatFlags, error) {
	s, err := m.get(id)
	if err != nil {
		return ChatFlags{}, err
	}
	return s.SetArchived(ctx, chatID, archive)
}

// SetChatPinned pins or unpins one of the session's chats.
func (m *Manager) SetChatPinned(ctx context.Context, id, chatID string, pin bool) (ChatFlags, error) {
	s, err := m.get(id)
	if err != nil {
		return ChatFlags{}, err
	}
	return s.SetPinned(ctx, chatID, pin)
}
