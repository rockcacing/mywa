// Package hub fans real-time events out to subscribers (WebSocket clients).
package hub

import (
	"sync"
	"time"
)

// Event is one frame sent to subscribers.
type Event struct {
	Event     string `json:"event"`
	Session   string `json:"session"`
	Timestamp int64  `json:"timestamp"`
	Data      any    `json:"data"`
}

// Event names.
const (
	SessionStatus   = "session.status"
	SessionQR       = "session.qr"
	MessageReceived = "message.received"
	MessageSent     = "message.sent"
	MessageAck      = "message.ack"
	MessageEdited   = "message.edited"
	MessageRevoked  = "message.revoked"
	ChatUpdated     = "chat.updated" // a chat was archived/unarchived or pinned/unpinned
	ChatsSynced     = "chats.synced" // archived/pinned state of all chats was refreshed
)

// BufferSize is how many events a subscriber may fall behind before it is dropped.
const BufferSize = 256

// Sub is one subscriber. C is closed when the subscriber is dropped or unsubscribed.
type Sub struct {
	C       <-chan Event
	c       chan Event
	session string // "" = all sessions
	once    sync.Once
}

type Hub struct {
	mu   sync.RWMutex
	subs map[*Sub]struct{}
}

func New() *Hub { return &Hub{subs: map[*Sub]struct{}{}} }

// Subscribe registers a subscriber for one session, or for all sessions when session is "".
func (h *Hub) Subscribe(session string) *Sub {
	c := make(chan Event, BufferSize)
	s := &Sub{C: c, c: c, session: session}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	s.once.Do(func() { close(s.c) })
}

// Publish delivers an event without blocking. A subscriber whose buffer is full is
// dropped (its channel closed) so one slow client can't stall the WhatsApp event loop.
func (h *Hub) Publish(session, event string, data any) {
	ev := Event{Event: event, Session: session, Timestamp: time.Now().Unix(), Data: data}
	var slow []*Sub
	h.mu.RLock()
	for s := range h.subs {
		if s.session != "" && s.session != session {
			continue
		}
		select {
		case s.c <- ev:
		default:
			slow = append(slow, s)
		}
	}
	h.mu.RUnlock()
	for _, s := range slow {
		h.Unsubscribe(s)
	}
}

// Len reports the number of subscribers.
func (h *Hub) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
