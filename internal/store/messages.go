package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Message status values. Outgoing messages only ever move forward through
// sent → delivered → read → played; incoming messages are "received".
const (
	StatusReceived  = "received"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusPlayed    = "played"
)

// TypeRevoked marks a message that was deleted for everyone.
const TypeRevoked = "revoked"

type Message struct {
	ID         string `json:"id"`
	ChatID     string `json:"chatId"`
	SenderID   string `json:"senderId"`
	SenderName string `json:"senderName,omitempty"`
	FromMe     bool   `json:"fromMe"`
	Timestamp  int64  `json:"timestamp"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	Status     string `json:"status"`
	QuotedID   string `json:"quotedId,omitempty"`
	Edited     bool   `json:"edited,omitempty"`
	// Media describes a downloadable attachment. It is nil for text messages and for
	// media messages stored before mywa kept download details.
	Media *MediaInfo `json:"media,omitempty"`
	// MediaBlob is the opaque download descriptor (a serialized WhatsApp message) the wa
	// package needs to fetch the file. It is written on save and never sent to clients.
	MediaBlob []byte `json:"-"`
}

// MediaInfo is what clients need to lay out an attachment before downloading it.
type MediaInfo struct {
	Mimetype string `json:"mimetype,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Size     uint64 `json:"size,omitempty"`
	Width    uint32 `json:"width,omitempty"`
	Height   uint32 `json:"height,omitempty"`
	Seconds  uint32 `json:"seconds,omitempty"`
	HasThumb bool   `json:"hasThumb,omitempty"`
}

func mediaInfoJSON(m *MediaInfo) string {
	if m == nil {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// SaveMessage inserts m and moves the chat's "last message" forward if m is newer.
// A message that is already stored is left alone and inserted is false.
// When countUnread is set, a newly stored message increments the chat's unread counter.
func (s *Store) SaveMessage(ctx context.Context, sessionID string, m Message, countUnread bool) (inserted bool, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (session_id, chat_jid, id, sender_jid, sender_name, from_me, ts, type, text, status, quoted_id, edited, media, media_info)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, chat_jid, id) DO NOTHING`,
		sessionID, m.ChatID, m.ID, m.SenderID, m.SenderName, b2i(m.FromMe), m.Timestamp,
		m.Type, m.Text, m.Status, m.QuotedID, b2i(m.Edited), m.MediaBlob, mediaInfoJSON(m.Media))
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already stored. A later copy (e.g. from a history sync) may carry download
		// details the first one lacked; keep them without touching anything else.
		if len(m.MediaBlob) > 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET media = ?, media_info = ?
				WHERE session_id = ? AND chat_jid = ? AND id = ? AND media IS NULL`,
				m.MediaBlob, mediaInfoJSON(m.Media), sessionID, m.ChatID, m.ID); err != nil {
				return false, err
			}
		}
		return false, tx.Commit()
	}

	unread := 0
	if countUnread && !m.FromMe {
		unread = 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO chats (session_id, jid, last_msg_id, last_msg_preview, last_msg_type, last_msg_from_me, last_msg_at, unread)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, jid) DO UPDATE SET
			last_msg_id      = CASE WHEN excluded.last_msg_at >= chats.last_msg_at THEN excluded.last_msg_id      ELSE chats.last_msg_id END,
			last_msg_preview = CASE WHEN excluded.last_msg_at >= chats.last_msg_at THEN excluded.last_msg_preview ELSE chats.last_msg_preview END,
			last_msg_type    = CASE WHEN excluded.last_msg_at >= chats.last_msg_at THEN excluded.last_msg_type    ELSE chats.last_msg_type END,
			last_msg_from_me = CASE WHEN excluded.last_msg_at >= chats.last_msg_at THEN excluded.last_msg_from_me ELSE chats.last_msg_from_me END,
			last_msg_at      = MAX(chats.last_msg_at, excluded.last_msg_at),
			unread           = chats.unread + excluded.unread`,
		sessionID, m.ChatID, m.ID, m.Text, m.Type, b2i(m.FromMe), m.Timestamp, unread)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

const statusRank = `CASE %s WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 WHEN 'played' THEN 4 ELSE 0 END`

func rank(expr string) string { return strings.Replace(statusRank, "%s", expr, 1) }

// UpdateStatus raises the status of our own messages; it never moves a status backwards.
// It returns the ids whose status actually changed.
func (s *Store) UpdateStatus(ctx context.Context, sessionID, chatJID string, ids []string, status string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var changed []string
	for _, id := range ids {
		res, err := s.DB.ExecContext(ctx, `
			UPDATE messages SET status = ?
			WHERE session_id = ? AND chat_jid = ? AND id = ? AND from_me = 1
			  AND `+rank("status")+` < `+rank("?"),
			status, sessionID, chatJID, id, status)
		if err != nil {
			return changed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = append(changed, id)
		}
	}
	return changed, nil
}

// EditMessage replaces a stored message's text. It reports whether the message was found.
func (s *Store) EditMessage(ctx context.Context, sessionID, chatJID, id, text string) (bool, error) {
	return s.rewrite(ctx, sessionID, chatJID, id, text, "")
}

// RevokeMessage marks a stored message as deleted for everyone and clears its text.
func (s *Store) RevokeMessage(ctx context.Context, sessionID, chatJID, id string) (bool, error) {
	return s.rewrite(ctx, sessionID, chatJID, id, "", TypeRevoked)
}

// rewrite sets a message's text and, when newType is "", flags it edited; otherwise it
// sets the message type to newType. The chat preview follows if this is its last message.
func (s *Store) rewrite(ctx context.Context, sessionID, chatJID, id, text, newType string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE messages SET
			text   = ?,
			edited = CASE WHEN ? = '' THEN 1 ELSE edited END,
			type   = CASE WHEN ? = '' THEN type ELSE ? END
		WHERE session_id = ? AND chat_jid = ? AND id = ?`,
		text, newType, newType, newType, sessionID, chatJID, id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE chats SET
			last_msg_preview = ?,
			last_msg_type    = CASE WHEN ? = '' THEN last_msg_type ELSE ? END
		WHERE session_id = ? AND jid = ? AND last_msg_id = ?`,
		text, newType, newType, sessionID, chatJID, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ListMessages returns up to limit messages of a chat older than before (unix seconds;
// 0 means no bound), newest first.
func (s *Store) ListMessages(ctx context.Context, sessionID, chatJID string, before int64, limit int) ([]Message, error) {
	q := `SELECT ` + messageCols + `
	      FROM messages WHERE session_id = ? AND chat_jid = ?`
	args := []any{sessionID, chatJID}
	if before > 0 {
		q += ` AND ts < ?`
		args = append(args, before)
	}
	q += ` ORDER BY ts DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMessage(ctx context.Context, sessionID, chatJID, id string) (Message, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT `+messageCols+`
		FROM messages WHERE session_id = ? AND chat_jid = ? AND id = ?`, sessionID, chatJID, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// GetMediaBlob returns the stored download descriptor of a message, or ErrNotFound when
// the message does not exist or has none.
func (s *Store) GetMediaBlob(ctx context.Context, sessionID, chatJID, id string) ([]byte, error) {
	var blob []byte
	err := s.DB.QueryRowContext(ctx,
		`SELECT media FROM messages WHERE session_id = ? AND chat_jid = ? AND id = ?`,
		sessionID, chatJID, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(blob) == 0) {
		return nil, ErrNotFound
	}
	return blob, err
}

const messageCols = `id, chat_jid, sender_jid, sender_name, from_me, ts, type, text, status, quoted_id, edited, media_info`

type scanner interface{ Scan(...any) error }

func scanMessage(r scanner) (Message, error) {
	var m Message
	var fromMe, edited int
	var info string
	err := r.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.SenderName, &fromMe, &m.Timestamp,
		&m.Type, &m.Text, &m.Status, &m.QuotedID, &edited, &info)
	m.FromMe, m.Edited = fromMe == 1, edited == 1
	if info != "" {
		var mi MediaInfo
		if json.Unmarshal([]byte(info), &mi) == nil {
			m.Media = &mi
		}
	}
	return m, err
}
