package store

import (
	"context"
	"database/sql"
	"errors"
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
	Archived    bool         `json:"archived"`
	Pinned      bool         `json:"pinned"`
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

// SetArchived records whether a chat is archived. lastMsgTS is the timestamp of the
// chat's newest message when it was archived (WhatsApp's "message range"): with the
// "keep chats archived" setting off, any newer message brings the chat back out.
func (s *Store) SetArchived(ctx context.Context, sessionID, jid string, archived bool, lastMsgTS int64) error {
	if !archived {
		lastMsgTS = 0
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO chats (session_id, jid, archived, archive_ts) VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id, jid) DO UPDATE SET archived = excluded.archived, archive_ts = excluded.archive_ts`,
		sessionID, jid, b2i(archived), lastMsgTS)
	return err
}

// SetUnarchiveChats stores the account's setting: true means a new message moves an
// archived chat back to the main list ("keep chats archived" off).
func (s *Store) SetUnarchiveChats(ctx context.Context, sessionID string, unarchive bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sessions SET unarchive_chats = ? WHERE id = ?`, b2i(unarchive), sessionID)
	return err
}

// isArchived is true for chats that are archived now: archived, and either the account
// keeps chats archived or nothing newer than the archived message range arrived since.
const isArchived = `(archived = 1 AND (last_msg_at <= archive_ts OR
	(SELECT unarchive_chats FROM sessions WHERE sessions.id = chats.session_id) = 0))`

// SetPinned records when a chat was pinned (unix seconds), or 0 when it is not pinned.
func (s *Store) SetPinned(ctx context.Context, sessionID, jid string, pinnedAt int64) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO chats (session_id, jid, pinned_at) VALUES (?, ?, ?)
		ON CONFLICT(session_id, jid) DO UPDATE SET pinned_at = excluded.pinned_at`,
		sessionID, jid, pinnedAt)
	return err
}

// ChatSummary counts the archived chats and how many of them have unread messages.
type ChatSummary struct {
	Archived       int `json:"archived"`
	ArchivedUnread int `json:"archivedUnread"`
}

func (s *Store) Summary(ctx context.Context, sessionID string) (ChatSummary, error) {
	var c ChatSummary
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(unread > 0), 0) FROM chats WHERE session_id = ? AND last_msg_at > 0 AND `+isArchived,
		sessionID).Scan(&c.Archived, &c.ArchivedUnread)
	return c, err
}

// ListChats returns the archived or the non-archived chats that have at least one stored
// message: pinned chats first (most recently pinned on top), then by newest activity.
// Chats known only from history or app state, with no messages, are left out.
func (s *Store) ListChats(ctx context.Context, sessionID string, archived bool, limit, offset int) ([]Chat, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT jid, name, unread, `+isArchived+`, pinned_at, last_msg_id, last_msg_preview, last_msg_type, last_msg_from_me, last_msg_at
		FROM chats WHERE session_id = ? AND last_msg_at > 0 AND `+isArchived+` = ?
		ORDER BY pinned_at DESC, last_msg_at DESC, jid LIMIT ? OFFSET ?`, sessionID, b2i(archived), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		var lm LastMessage
		var fromMe, arch int
		var pinnedAt int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Unread, &arch, &pinnedAt, &lm.ID, &lm.Text, &lm.Type, &fromMe, &lm.Timestamp); err != nil {
			return nil, err
		}
		c.IsGroup = strings.HasSuffix(c.ID, "@g.us")
		c.Archived, c.Pinned = arch == 1, pinnedAt > 0
		if lm.ID != "" {
			lm.FromMe = fromMe == 1
			c.LastMessage = &lm
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChatState is what archive/pin actions need to know about one chat.
type ChatState struct {
	Archived      bool
	Pinned        bool
	LastMessageID string
	LastMessageTS int64
}

func (s *Store) GetChatState(ctx context.Context, sessionID, jid string) (ChatState, error) {
	var c ChatState
	var arch int
	var pinnedAt int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT `+isArchived+`, pinned_at, last_msg_id, last_msg_at FROM chats WHERE session_id = ? AND jid = ?`,
		sessionID, jid).Scan(&arch, &pinnedAt, &c.LastMessageID, &c.LastMessageTS)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.Archived, c.Pinned = arch == 1, pinnedAt > 0
	return c, err
}

// PinnedCount counts the session's pinned chats.
func (s *Store) PinnedCount(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE session_id = ? AND pinned_at > 0`, sessionID).Scan(&n)
	return n, err
}
