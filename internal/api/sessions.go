package api

import (
	"net/http"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/rockcacing/mywa/internal/wa"
)

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	info, err := s.mgr.Create(r.Context(), body.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.List())
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	info, err := s.mgr.Get(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	info, err := s.mgr.Connect(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// qr returns the current QR code as JSON {"code": "..."} or, with ?format=png, as an image.
func (s *Server) qr(w http.ResponseWriter, r *http.Request) {
	code, err := s.mgr.QR(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "png" {
		png, err := qrcode.Encode(code, qrcode.Medium, 512)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

func (s *Server) pairingCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	code, err := s.mgr.PairingCode(r.Context(), r.PathValue("id"), body.Phone)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	info, err := s.mgr.Disconnect(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	info, err := s.mgr.Logout(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

var _ Manager = (*wa.Manager)(nil)
