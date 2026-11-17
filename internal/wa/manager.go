// Package wa runs WhatsApp sessions on top of whatsmeow and keeps the store and
// event hub in step with them.
package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"github.com/rockcacing/mywa/internal/hub"
	mystore "github.com/rockcacing/mywa/internal/store"
)

// Errors returned to API callers. The API layer maps them to HTTP statuses.
var (
	ErrNotFound = errors.New("session not found")
	ErrExists   = errors.New("session already exists")
	ErrInvalid  = errors.New("invalid request")
	ErrNotReady = errors.New("session is not connected")
	ErrNoQR     = errors.New("no QR code available")
	ErrState    = errors.New("operation not allowed in the current session state")
	ErrUpstream = errors.New("WhatsApp request failed")
)

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Info describes a session to API callers.
type Info struct {
	ID        string `json:"id"`
	Status    Status `json:"status"`
	JID       string `json:"jid,omitempty"`
	PushName  string `json:"pushName,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// Manager owns every session.
type Manager struct {
	db        *mystore.Store
	container *sqlstore.Container
	hub       *hub.Hub
	log       *slog.Logger
	mediaDir  string // cache for downloaded attachments

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager prepares whatsmeow's device store inside db and loads the saved sessions.
// Call Restore afterwards to reconnect the ones that were online.
func NewManager(ctx context.Context, db *mystore.Store, h *hub.Hub, mediaDir string, log *slog.Logger) (*Manager, error) {
	store.SetOSInfo("mywa", [3]uint32{0, 1, 0})
	container := sqlstore.NewWithDB(db.DB, "sqlite", newWALogger(log.With("component", "whatsmeow-store")))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade whatsmeow store: %w", err)
	}
	m := &Manager{db: db, container: container, hub: h, mediaDir: mediaDir, log: log, sessions: map[string]*Session{}}
	rows, err := db.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		m.sessions[r.ID] = newSession(m, r)
	}
	return m, nil
}

// Restore reconnects sessions that were linked and online when the process stopped.
// Sessions the user disconnected on purpose stay disconnected.
func (m *Manager) Restore(ctx context.Context) {
	for _, s := range m.all() {
		if s.shouldRestore() {
			go func() {
				if _, err := s.Connect(ctx); err != nil {
					m.log.Warn("restore session", "session", s.id, "err", err)
				}
			}()
		}
	}
}

// Close disconnects every session without changing its saved status, so Restore picks
// them up again on the next start.
func (m *Manager) Close() {
	for _, s := range m.all() {
		s.shutdown()
	}
}

func (m *Manager) all() []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out
}

func (m *Manager) get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

func (m *Manager) Create(ctx context.Context, id string) (Info, error) {
	if !sessionIDRe.MatchString(id) {
		return Info{}, fmt.Errorf("%w: id must be 1-64 characters of A-Z, a-z, 0-9, _ or -", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; ok {
		return Info{}, ErrExists
	}
	row, err := m.db.CreateSession(ctx, id)
	if errors.Is(err, mystore.ErrExists) {
		return Info{}, ErrExists
	} else if err != nil {
		return Info{}, err
	}
	s := newSession(m, row)
	m.sessions[id] = s
	return s.Info(), nil
}

func (m *Manager) List() []Info {
	all := m.all()
	out := make([]Info, 0, len(all))
	for _, s := range all {
		out = append(out, s.Info())
	}
	return out
}

func (m *Manager) Get(id string) (Info, error) {
	s, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	return s.Info(), nil
}

func (m *Manager) Connect(ctx context.Context, id string) (Info, error) {
	s, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	return s.Connect(ctx)
}

func (m *Manager) QR(id string) (string, error) {
	s, err := m.get(id)
	if err != nil {
		return "", err
	}
	return s.QR()
}

func (m *Manager) PairingCode(ctx context.Context, id, phone string) (string, error) {
	s, err := m.get(id)
	if err != nil {
		return "", err
	}
	return s.PairingCode(ctx, phone)
}

func (m *Manager) Disconnect(id string) (Info, error) {
	s, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	return s.Disconnect(), nil
}

func (m *Manager) Logout(ctx context.Context, id string) (Info, error) {
	s, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	return s.Logout(ctx)
}

// Delete logs the session out (best effort) and removes it with all its data.
func (m *Manager) Delete(ctx context.Context, id string) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	if s.Info().JID != "" {
		if _, err := s.Logout(ctx); err != nil {
			m.log.Warn("logout during delete", "session", id, "err", err)
		}
	}
	s.shutdown() // stops a pending QR login, or anything Logout left running
	if err := m.db.DeleteSession(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	m.removeMediaCache(id)
	return nil
}

// Media returns a message's attachment, or with thumb its embedded preview image.
func (m *Manager) Media(ctx context.Context, id, chatID, msgID string, thumb bool) (Media, error) {
	s, err := m.get(id)
	if err != nil {
		return Media{}, err
	}
	return s.Media(ctx, chatID, msgID, thumb)
}

// SendTextRequest is the body of POST /sessions/{id}/messages/send-text.
type SendTextRequest struct {
	ChatID  string `json:"chatId"`
	Text    string `json:"text"`
	ReplyTo string `json:"replyTo,omitempty"`
}

func (m *Manager) SendText(ctx context.Context, id string, req SendTextRequest) (mystore.Message, error) {
	s, err := m.get(id)
	if err != nil {
		return mystore.Message{}, err
	}
	return s.SendText(ctx, req)
}

// ListChats returns the session's archived or non-archived chats, pinned chats first.
func (m *Manager) ListChats(ctx context.Context, id string, archived bool, limit, offset int) ([]mystore.Chat, error) {
	if _, err := m.get(id); err != nil {
		return nil, err
	}
	return m.db.ListChats(ctx, id, archived, limit, offset)
}

// ChatSummary counts the session's archived chats.
func (m *Manager) ChatSummary(ctx context.Context, id string) (mystore.ChatSummary, error) {
	if _, err := m.get(id); err != nil {
		return mystore.ChatSummary{}, err
	}
	return m.db.Summary(ctx, id)
}

func (m *Manager) ListMessages(ctx context.Context, id, chatID string, before int64, limit int) ([]mystore.Message, error) {
	s, err := m.get(id)
	if err != nil {
		return nil, err
	}
	jid, err := ParseChatID(chatID)
	if err != nil {
		return nil, err
	}
	jid = s.canonicalChat(ctx, jid, types.EmptyJID)
	return m.db.ListMessages(ctx, id, jid.String(), before, limit)
}

// ParseChatID accepts a full JID ("62812...@s.whatsapp.net", "...@g.us", "...@lid") or a
// phone number in international format ("+62 812-345", "62812345") and returns a chat JID.
func ParseChatID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.EmptyJID, fmt.Errorf("%w: chatId is required", ErrInvalid)
	}
	if strings.Contains(s, "@") {
		jid, err := types.ParseJID(s)
		if err != nil || jid.User == "" {
			return types.EmptyJID, fmt.Errorf("%w: chatId %q is not a valid JID", ErrInvalid, s)
		}
		switch jid.Server {
		case types.DefaultUserServer, types.GroupServer, types.HiddenUserServer:
			return jid.ToNonAD(), nil
		}
		return types.EmptyJID, fmt.Errorf("%w: chatId server %q is not supported", ErrInvalid, jid.Server)
	}
	digits := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			return -1
		}
		return 'x'
	}, s)
	if strings.Contains(digits, "x") || len(digits) < 6 || len(digits) > 15 {
		return types.EmptyJID, fmt.Errorf("%w: chatId %q is not a phone number or JID", ErrInvalid, s)
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

func nowUnix() int64 { return time.Now().Unix() }

// Revoke deletes one of the session's own messages for everyone.
func (m *Manager) Revoke(ctx context.Context, id, chatID, msgID string) (mystore.Message, error) {
	s, err := m.get(id)
	if err != nil {
		return mystore.Message{}, err
	}
	return s.Revoke(ctx, chatID, msgID)
}
