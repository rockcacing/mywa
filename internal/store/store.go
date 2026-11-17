// Package store persists sessions, chats and messages in SQLite.
//
// whatsmeow keeps its own device and encryption tables (whatsmeow_*) in the same
// database file; this package only owns the tables created in schema below.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	jid        TEXT NOT NULL DEFAULT '',
	push_name  TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL DEFAULT 'created',
	unarchive_chats INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chats (
	session_id       TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	jid              TEXT    NOT NULL,
	name             TEXT    NOT NULL DEFAULT '',
	last_msg_id      TEXT    NOT NULL DEFAULT '',
	last_msg_preview TEXT    NOT NULL DEFAULT '',
	last_msg_type    TEXT    NOT NULL DEFAULT '',
	last_msg_from_me INTEGER NOT NULL DEFAULT 0,
	last_msg_at      INTEGER NOT NULL DEFAULT 0,
	unread           INTEGER NOT NULL DEFAULT 0,
	archived         INTEGER NOT NULL DEFAULT 0,
	pinned_at        INTEGER NOT NULL DEFAULT 0,
	archive_ts       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_id, jid)
);
CREATE INDEX IF NOT EXISTS chats_recent ON chats(session_id, last_msg_at DESC);

CREATE TABLE IF NOT EXISTS messages (
	session_id  TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	chat_jid    TEXT    NOT NULL,
	id          TEXT    NOT NULL,
	sender_jid  TEXT    NOT NULL DEFAULT '',
	sender_name TEXT    NOT NULL DEFAULT '',
	from_me     INTEGER NOT NULL DEFAULT 0,
	ts          INTEGER NOT NULL,
	type        TEXT    NOT NULL,
	text        TEXT    NOT NULL DEFAULT '',
	status      TEXT    NOT NULL DEFAULT '',
	quoted_id   TEXT    NOT NULL DEFAULT '',
	edited      INTEGER NOT NULL DEFAULT 0,
	media       BLOB,
	media_info  TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, chat_jid, id)
);
CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages(session_id, chat_jid, ts DESC);
`

// Store wraps the shared *sql.DB.
type Store struct {
	DB *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Columns added after the first release; CREATE TABLE IF NOT EXISTS skips existing tables.
	for _, c := range []struct{ table, col, decl string }{
		{"messages", "media", "BLOB"},
		{"messages", "media_info", "TEXT NOT NULL DEFAULT ''"},
		{"chats", "archived", "INTEGER NOT NULL DEFAULT 0"},
		{"chats", "pinned_at", "INTEGER NOT NULL DEFAULT 0"},
		{"chats", "archive_ts", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "unarchive_chats", "INTEGER NOT NULL DEFAULT 1"},
	} {
		if err := addColumn(ctx, db, c.table, c.col, c.decl); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate %s.%s: %w", c.table, c.col, err)
		}
	}
	return &Store{DB: db}, nil
}

func addColumn(ctx context.Context, db *sql.DB, table, col, decl string) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+col+` `+decl)
	return err
}

func (s *Store) Close() error { return s.DB.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
