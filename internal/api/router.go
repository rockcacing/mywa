// Package api exposes the session manager over HTTP and WebSocket.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/rockcacing/mywa/internal/hub"
	"github.com/rockcacing/mywa/internal/store"
	"github.com/rockcacing/mywa/internal/wa"
)

// Manager is the part of *wa.Manager the API uses; tests substitute a fake.
type Manager interface {
	Create(ctx context.Context, id string) (wa.Info, error)
	List() []wa.Info
	Get(id string) (wa.Info, error)
	Connect(ctx context.Context, id string) (wa.Info, error)
	QR(id string) (string, error)
	PairingCode(ctx context.Context, id, phone string) (string, error)
	Disconnect(id string) (wa.Info, error)
	Logout(ctx context.Context, id string) (wa.Info, error)
	Delete(ctx context.Context, id string) error
	SendText(ctx context.Context, id string, req wa.SendTextRequest) (store.Message, error)
	ListChats(ctx context.Context, id string, archived bool, limit, offset int) ([]store.Chat, error)
	ChatSummary(ctx context.Context, id string) (store.ChatSummary, error)
	ListMessages(ctx context.Context, id, chatID string, before int64, limit int) ([]store.Message, error)
	Media(ctx context.Context, id, chatID, msgID string, thumb bool) (wa.Media, error)
	Revoke(ctx context.Context, id, chatID, msgID string) (store.Message, error)
	SetChatArchived(ctx context.Context, id, chatID string, archive bool) (wa.ChatFlags, error)
	SetChatPinned(ctx context.Context, id, chatID string, pin bool) (wa.ChatFlags, error)
}

type Server struct {
	mgr    Manager
	hub    *hub.Hub
	apiKey string
	log    *slog.Logger
}

func New(mgr Manager, h *hub.Hub, apiKey string, log *slog.Logger) *Server {
	return &Server{mgr: mgr, hub: h, apiKey: apiKey, log: log}
}

// Handler returns the full HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	mux.HandleFunc("POST /sessions", s.createSession)
	mux.HandleFunc("GET /sessions", s.listSessions)
	mux.HandleFunc("GET /sessions/{id}", s.getSession)
	mux.HandleFunc("DELETE /sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /sessions/{id}/connect", s.connect)
	mux.HandleFunc("GET /sessions/{id}/qr", s.qr)
	mux.HandleFunc("POST /sessions/{id}/pairing-code", s.pairingCode)
	mux.HandleFunc("POST /sessions/{id}/disconnect", s.disconnect)
	mux.HandleFunc("POST /sessions/{id}/logout", s.logout)

	mux.HandleFunc("GET /sessions/{id}/chats", s.listChats)
	mux.HandleFunc("GET /sessions/{id}/chats/summary", s.chatSummary)
	mux.HandleFunc("GET /sessions/{id}/chats/{chatId}/messages", s.listMessages)
	mux.HandleFunc("GET /sessions/{id}/chats/{chatId}/messages/{msgId}/media", s.media)
	mux.HandleFunc("DELETE /sessions/{id}/chats/{chatId}/messages/{msgId}", s.revoke)
	mux.HandleFunc("POST /sessions/{id}/chats/{chatId}/archive", s.chatAction(true, true))
	mux.HandleFunc("POST /sessions/{id}/chats/{chatId}/unarchive", s.chatAction(true, false))
	mux.HandleFunc("POST /sessions/{id}/chats/{chatId}/pin", s.chatAction(false, true))
	mux.HandleFunc("POST /sessions/{id}/chats/{chatId}/unpin", s.chatAction(false, false))
	mux.HandleFunc("POST /sessions/{id}/messages/send-text", s.sendText)

	mux.HandleFunc("GET /events", s.events)

	return s.recoverer(s.logRequests(s.auth(mux)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
