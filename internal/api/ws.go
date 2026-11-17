package api

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	pingInterval = 30 * time.Second
	writeTimeout = 10 * time.Second
)

// events streams hub events over a WebSocket. ?session=<id> limits the stream to one session.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if session != "" {
		if _, err := s.mgr.Get(session); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Auth is by API key, not cookies, so any origin may connect.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return // Accept already wrote the error response
	}
	defer c.CloseNow()

	sub := s.hub.Subscribe(session)
	defer s.hub.Unsubscribe(sub)

	// Clients only listen; CloseRead discards anything they send and cancels ctx on close.
	ctx := c.CloseRead(r.Context())
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				c.Close(websocket.StatusPolicyViolation, "client too slow; events dropped")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := wsjson.Write(wctx, c, ev)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
