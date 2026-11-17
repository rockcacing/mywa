package store

import (
	"context"
	"strings"
)

type LastMessage struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	FromMe    bool   `json:"fromMe"`
	Timestamp int64  `json:"timestamp"`
}

type Chat struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	IsGroup     bool         `json:"isGroup"`
	Unread      int          `json:"unread"`
	LastMessage *LastMessage `json:"lastMessage"`
}

// UpsertChat makes sure the chat exists. A non-empty name replaces the stored one;
// a non-nil unread replaces the unread counter.
func (s *Store) UpsertChat(ctx context.Context, sessionID, jid, name string, unread *int) error {
	u, setUnread := 0, 0
	if unread != nil {
		u, setUnread = *unread, 1
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO chats (session_id, jid, name, unread) VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id, jid) DO UPDATE SET
			name   = CASE WHEN excluded.name <> '' THEN excluded.name ELSE chats.name END,
			unread = CASE WHEN ? = 1 THEN excluded.unread ELSE chats.unread END`,
		sessionID, jid, name, u, setUnread)
	return err
}

// ChatName returns the stored name of a chat, or "" if the chat or its name is unknown.
func (s *Store) ChatName(ctx context.Context, sessionID, jid string) string {
	var name string
	_ = s.DB.QueryRowContext(ctx, `SELECT name FROM chats WHERE session_id = ? AND jid = ?`, sessionID, jid).Scan(&name)
	return name
}

func (s *Store) ResetUnread(ctx context.Context, sessionID, jid string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE chats SET unread = 0 WHERE session_id = ? AND jid = ?`, sessionID, jid)
	return err
}

// ListChats returns chats newest-activity first. Chats with no message yet sort last.
func (s *Store) ListChats(ctx context.Context, sessionID string, limit, offset int) ([]Chat, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT jid, name, unread, last_msg_id, last_msg_preview, last_msg_type, last_msg_from_me, last_msg_at
		FROM chats WHERE session_id = ?
		ORDER BY last_msg_at DESC, jid LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		var lm LastMessage
		var fromMe int
		if err := rows.Scan(&c.ID, &c.Name, &c.Unread, &lm.ID, &lm.Text, &lm.Type, &fromMe, &lm.Timestamp); err != nil {
			return nil, err
		}
		c.IsGroup = strings.HasSuffix(c.ID, "@g.us")
		if lm.ID != "" {
			lm.FromMe = fromMe == 1
			c.LastMessage = &lm
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
