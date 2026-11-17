package api

import (
	"net/http"

	"github.com/rockcacing/mywa/internal/wa"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", defaultLimit, maxLimit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	offset, err := intParam(r, "offset", 0, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limit == 0 {
		limit = defaultLimit
	}
	chats, err := s.mgr.ListChats(r.Context(), r.PathValue("id"), int(limit), int(offset))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, chats)
}

// listMessages returns a chat's messages newest first. Page backwards with
// ?before=<timestamp of the oldest message you have>.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", defaultLimit, maxLimit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	before, err := intParam(r, "before", 0, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limit == 0 {
		limit = defaultLimit
	}
	msgs, err := s.mgr.ListMessages(r.Context(), r.PathValue("id"), r.PathValue("chatId"), before, int(limit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) sendText(w http.ResponseWriter, r *http.Request) {
	var req wa.SendTextRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	msg, err := s.mgr.SendText(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}
