package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/rockcacing/mywa/internal/hub"
	mystore "github.com/rockcacing/mywa/internal/store"
)

// handleEvent is registered on each whatsmeow client. whatsmeow calls it synchronously
// from its receive loop, so slow work (history sync, group lookups) runs in goroutines.
func (s *Session) handleEvent(cli *whatsmeow.Client, evt any) {
	if !s.isCurrent(cli) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch e := evt.(type) {
	case *events.PairSuccess:
		s.setAccount(e.ID.String(), "")
		s.setStatus(StatusConnecting, "")

	case *events.Connected:
		if cli.Store.ID != nil {
			s.setAccount(cli.Store.ID.String(), cli.Store.PushName)
		}
		s.setStatus(StatusReady, "")

	case *events.PushNameSetting:
		if cli.Store.ID != nil {
			s.setAccount(cli.Store.ID.String(), cli.Store.PushName)
		}

	case *events.Disconnected:
		// whatsmeow reconnects on its own (EnableAutoReconnect).
		s.setStatus(StatusConnecting, "connection lost; reconnecting")

	case *events.LoggedOut:
		s.unlink(ctx, true)
		s.setStatus(StatusLoggedOut, fmt.Sprintf("logged out by WhatsApp (reason %d); connect again to link", e.Reason))

	case *events.StreamReplaced:
		s.stop(cli)
		s.setStatus(StatusFailed, "another client connected with this session's credentials")

	case *events.TemporaryBan:
		s.stop(cli)
		s.setStatus(StatusFailed, e.String())

	case *events.ClientOutdated:
		s.stop(cli)
		s.setStatus(StatusFailed, "WhatsApp rejected the client as outdated; update go.mau.fi/whatsmeow")

	case *events.ConnectFailure:
		s.stop(cli)
		s.setStatus(StatusFailed, fmt.Sprintf("connect failure %d: %s", e.Reason, e.Message))

	case *events.PairError:
		s.setStatus(StatusFailed, fmt.Sprintf("pairing failed: %v", e.Error))

	case *events.Message:
		s.onMessage(ctx, cli, e, true)

	case *events.Receipt:
		s.onReceipt(ctx, e)

	case *events.MarkChatAsRead:
		if e.Action.GetRead() {
			chat := s.canonicalChat(ctx, e.JID, types.EmptyJID)
			if err := s.m.db.ResetUnread(ctx, s.id, chat.String()); err != nil {
				s.log.Error("reset unread", "err", err)
			}
		}

	case *events.HistorySync:
		go s.onHistorySync(cli, e)
	}
}

// supportedChat filters out status updates, broadcast lists and newsletters.
func supportedChat(j types.JID) bool {
	switch j.Server {
	case types.DefaultUserServer, types.GroupServer, types.HiddenUserServer:
		return true
	}
	return false
}

// onMessage stores a message and, for live messages, publishes it.
func (s *Session) onMessage(ctx context.Context, cli *whatsmeow.Client, e *events.Message, live bool) {
	info := e.Info
	if !supportedChat(info.Chat) {
		return
	}
	alt := info.SenderAlt
	if info.IsFromMe {
		alt = info.RecipientAlt
	}
	if info.IsGroup {
		alt = types.EmptyJID
	}
	chat := s.canonicalChat(ctx, info.Chat, alt)
	sender := s.canonicalChat(ctx, info.Sender, info.SenderAlt)

	if pm := e.Message.GetProtocolMessage(); pm != nil {
		s.onProtocolMessage(ctx, chat, pm, live)
		return
	}
	content, ok := Extract(e.Message)
	if !ok {
		return
	}

	m := mystore.Message{
		ID:        info.ID,
		ChatID:    chat.String(),
		SenderID:  sender.String(),
		FromMe:    info.IsFromMe,
		Timestamp: info.Timestamp.Unix(),
		Type:      content.Type,
		Text:      content.Text,
		Status:    mystore.StatusReceived,
		QuotedID:  content.QuotedID,
	}
	if info.IsFromMe {
		m.Status = mystore.StatusSent
		m.SenderName = s.Info().PushName
	} else {
		m.SenderName = info.PushName
	}

	inserted, err := s.m.db.SaveMessage(ctx, s.id, m, live)
	if err != nil {
		s.log.Error("store message", "err", err, "id", m.ID)
		return
	}
	if !inserted || !live {
		return
	}
	pushName := ""
	if !info.IsFromMe && !info.IsGroup {
		pushName = info.PushName
	}
	s.ensureChatName(ctx, cli, chat, pushName)
	if info.IsFromMe {
		s.m.hub.Publish(s.id, hub.MessageSent, m)
	} else {
		s.m.hub.Publish(s.id, hub.MessageReceived, m)
	}
}

func (s *Session) onProtocolMessage(ctx context.Context, chat types.JID, pm *waE2E.ProtocolMessage, live bool) {
	target := pm.GetKey().GetID()
	if target == "" {
		return
	}
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		found, err := s.m.db.RevokeMessage(ctx, s.id, chat.String(), target)
		if err != nil {
			s.log.Error("revoke message", "err", err)
		}
		if found && live {
			s.m.hub.Publish(s.id, hub.MessageRevoked, map[string]any{"chatId": chat.String(), "id": target})
		}
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		content, ok := Extract(pm.GetEditedMessage())
		if !ok {
			return
		}
		found, err := s.m.db.EditMessage(ctx, s.id, chat.String(), target, content.Text)
		if err != nil {
			s.log.Error("edit message", "err", err)
		}
		if found && live {
			s.m.hub.Publish(s.id, hub.MessageEdited, map[string]any{"chatId": chat.String(), "id": target, "text": content.Text})
		}
	}
}

func (s *Session) onReceipt(ctx context.Context, e *events.Receipt) {
	if e.IsFromMe || !supportedChat(e.Chat) {
		return // our own other devices reading messages, or status/broadcast receipts
	}
	var status string
	switch e.Type {
	case types.ReceiptTypeDelivered:
		status = mystore.StatusDelivered
	case types.ReceiptTypeRead:
		status = mystore.StatusRead
	case types.ReceiptTypePlayed:
		status = mystore.StatusPlayed
	default:
		return
	}
	alt := e.SenderAlt
	if e.IsGroup {
		alt = types.EmptyJID
	}
	chat := s.canonicalChat(ctx, e.Chat, alt)
	changed, err := s.m.db.UpdateStatus(ctx, s.id, chat.String(), e.MessageIDs, status)
	if err != nil {
		s.log.Error("update status", "err", err)
	}
	if len(changed) > 0 {
		s.m.hub.Publish(s.id, hub.MessageAck, map[string]any{
			"chatId": chat.String(), "ids": changed, "status": status, "timestamp": e.Timestamp.Unix(),
		})
	}
}

// onHistorySync stores the chats and messages the phone sends after linking.
func (s *Session) onHistorySync(cli *whatsmeow.Client, e *events.HistorySync) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	ctx := context.Background()
	convs := e.Data.GetConversations()
	nMsgs := 0
	for _, conv := range convs {
		if !s.isCurrent(cli) {
			return
		}
		raw, err := types.ParseJID(conv.GetID())
		if err != nil || !supportedChat(raw) {
			continue
		}
		chat := s.canonicalChat(ctx, raw, types.EmptyJID)
		name := firstNonEmpty(conv.GetName(), conv.GetDisplayName())
		if name == "" && chat.Server != types.GroupServer {
			name = contactName(ctx, cli, chat)
		}
		unread := int(conv.GetUnreadCount())
		if err := s.m.db.UpsertChat(ctx, s.id, chat.String(), name, &unread); err != nil {
			s.log.Error("history chat", "err", err)
			continue
		}
		for _, hm := range conv.GetMessages() {
			evt, err := cli.ParseWebMessage(raw, hm.GetMessage())
			if err != nil {
				continue
			}
			s.onMessage(ctx, cli, evt, false)
			nMsgs++
		}
		if name == "" && chat.Server == types.GroupServer {
			s.ensureChatName(ctx, cli, chat, "")
		}
	}
	s.log.Info("history sync", "type", e.Data.GetSyncType().String(), "chats", len(convs), "messages", nMsgs)
}

// ensureChatName fills in a chat's name the first time it is seen: the group subject for
// groups, otherwise the saved contact name, falling back to the sender's push name.
func (s *Session) ensureChatName(ctx context.Context, cli *whatsmeow.Client, chat types.JID, pushName string) {
	if s.m.db.ChatName(ctx, s.id, chat.String()) != "" {
		return
	}
	if chat.Server == types.GroupServer {
		if _, started := s.groupLookups.LoadOrStore(chat.String(), struct{}{}); started {
			return
		}
		go func() {
			gctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			gi, err := cli.GetGroupInfo(gctx, chat)
			if err != nil || gi.Name == "" {
				s.groupLookups.Delete(chat.String()) // retry on a later message
				return
			}
			if err := s.m.db.UpsertChat(gctx, s.id, chat.String(), gi.Name, nil); err != nil {
				s.log.Error("store group name", "err", err)
			}
		}()
		return
	}
	name := firstNonEmpty(contactName(ctx, cli, chat), pushName)
	if name != "" {
		if err := s.m.db.UpsertChat(ctx, s.id, chat.String(), name, nil); err != nil {
			s.log.Error("store chat name", "err", err)
		}
	}
}

func contactName(ctx context.Context, cli *whatsmeow.Client, jid types.JID) string {
	if cli.Store.Contacts == nil {
		return ""
	}
	c, err := cli.Store.Contacts.GetContact(ctx, jid)
	if err != nil || !c.Found {
		return ""
	}
	return firstNonEmpty(c.FullName, c.FirstName, c.BusinessName, c.PushName)
}
