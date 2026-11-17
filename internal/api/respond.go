package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/rockcacing/mywa/internal/wa"
)

const maxBodyBytes = 1 << 20

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: msg, Code: code})
}

// fail maps manager errors to HTTP responses.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, wa.ErrNotFound):
		writeError(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	case errors.Is(err, wa.ErrExists):
		writeError(w, http.StatusConflict, "SESSION_EXISTS", err.Error())
	case errors.Is(err, wa.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, wa.ErrNotReady):
		writeError(w, http.StatusConflict, "SESSION_NOT_READY", err.Error())
	case errors.Is(err, wa.ErrState):
		writeError(w, http.StatusConflict, "INVALID_STATE", err.Error())
	case errors.Is(err, wa.ErrNoQR):
		writeError(w, http.StatusNotFound, "NO_QR", "no QR code available; connect the session and wait for status qr_ready")
	case errors.Is(err, wa.ErrNoMedia):
		writeError(w, http.StatusNotFound, "NO_MEDIA", err.Error())
	case errors.Is(err, wa.ErrMediaExpired):
		writeError(w, http.StatusGone, "MEDIA_EXPIRED", err.Error())
	case errors.Is(err, wa.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", err.Error())
	case errors.Is(err, wa.ErrUpstream):
		writeError(w, http.StatusBadGateway, "WHATSAPP_ERROR", err.Error())
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}

// decode reads a JSON body into v.
func decode(r *http.Request, v any) error {
	body := io.LimitReader(r.Body, maxBodyBytes+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("%w: read body: %v", wa.ErrInvalid, err)
	}
	if len(data) > maxBodyBytes {
		return fmt.Errorf("%w: body larger than %d bytes", wa.ErrInvalid, maxBodyBytes)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: body is not valid JSON: %v", wa.ErrInvalid, err)
	}
	return nil
}

// intParam parses an optional non-negative integer query parameter.
func intParam(r *http.Request, name string, def, max int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be a non-negative integer", wa.ErrInvalid, name)
	}
	if max > 0 && n > max {
		n = max
	}
	return n, nil
}
