package api

import (
	"fmt"
	"mime"
	"net/http"
	"strconv"

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
	archived := false
	switch r.URL.Query().Get("archived") {
	case "", "false", "0":
	case "true", "1":
		archived = true
	default:
		s.fail(w, r, fmt.Errorf("%w: archived must be true or false", wa.ErrInvalid))
		return
	}
	chats, err := s.mgr.ListChats(r.Context(), r.PathValue("id"), archived, int(limit), int(offset))
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

// media returns a message's attachment; ?thumb=1 returns its small embedded preview.
// Only images, audio and video are served for inline display. Anything else (documents,
// SVG, HTML…) is sent as an attachment, and the sandbox CSP stops a file opened
// directly from running script on this origin.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	thumb := r.URL.Query().Get("thumb") == "1"
	m, err := s.mgr.Media(r.Context(), r.PathValue("id"), r.PathValue("chatId"), r.PathValue("msgId"), thumb)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	ctype := m.Mimetype
	if !wa.SafeInlineType(ctype) {
		ctype = "application/octet-stream"
		name := m.FileName
		if name == "" {
			name = "file"
		}
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(m.Data)))
	_, _ = w.Write(m.Data)
}

// revoke deletes one of your own messages for everyone in the chat.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	msg, err := s.mgr.Revoke(r.Context(), r.PathValue("id"), r.PathValue("chatId"), r.PathValue("msgId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// chatSummary returns counts for the "Archived" entry of a chat list.
func (s *Server) chatSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.mgr.ChatSummary(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// chatAction serves archive/unarchive (archive=true) and pin/unpin (archive=false).
func (s *Server) chatAction(archive, on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			res wa.ChatFlags
			err error
		)
		if archive {
			res, err = s.mgr.SetChatArchived(r.Context(), r.PathValue("id"), r.PathValue("chatId"), on)
		} else {
			res, err = s.mgr.SetChatPinned(r.Context(), r.PathValue("id"), r.PathValue("chatId"), on)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
