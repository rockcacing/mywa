package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type SessionRow struct {
	ID        string
	JID       string
	PushName  string
	Status    string
	CreatedAt time.Time
}

// ErrExists is returned by CreateSession when the id is taken.
var ErrExists = errors.New("already exists")

func (s *Store) CreateSession(ctx context.Context, id string) (SessionRow, error) {
	row := SessionRow{ID: id, Status: "created", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO sessions (id, status, created_at) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		row.ID, row.Status, row.CreatedAt.Unix())
	if err != nil {
		return row, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return row, ErrExists
	}
	return row, nil
}

func (s *Store) ListSessions(ctx context.Context) ([]SessionRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, jid, push_name, status, created_at FROM sessions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var created int64
		if err := rows.Scan(&r.ID, &r.JID, &r.PushName, &r.Status, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetSession(ctx context.Context, id string) (SessionRow, error) {
	var r SessionRow
	var created int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, jid, push_name, status, created_at FROM sessions WHERE id = ?`, id).
		Scan(&r.ID, &r.JID, &r.PushName, &r.Status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CreatedAt = time.Unix(created, 0).UTC()
	return r, err
}

func (s *Store) SetSessionStatus(ctx context.Context, id, status string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sessions SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) SetSessionAccount(ctx context.Context, id, jid, pushName string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE sessions SET jid = ?, push_name = ? WHERE id = ?`, jid, pushName, id)
	return err
}

// DeleteSession removes the session and, through ON DELETE CASCADE, its chats and messages.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// PurgeSessionData removes a session's chats and messages but keeps the session row.
func (s *Store) PurgeSessionData(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chats WHERE session_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
