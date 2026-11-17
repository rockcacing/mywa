package api

import (
	_ "embed"
	"net/http"
)

//go:embed web/index.html
var indexHTML []byte

// index serves the link page. The page itself is public; everything it does goes
// through the API with the key the visitor enters (or passes as #key=... in the URL).
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src blob:; "+
			"connect-src 'self' wss://"+r.Host+" ws://"+r.Host+"; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write(indexHTML)
}
