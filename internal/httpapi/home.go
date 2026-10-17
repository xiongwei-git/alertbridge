package httpapi

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
)

//go:embed static/service-home.html
var publicHome string

var publicHomeCSP = func() string {
	_, rest, ok := strings.Cut(publicHome, "<style>")
	style, _, end := strings.Cut(rest, "</style>")
	if !ok || !end {
		panic("embedded public home must contain its style block")
	}
	sum := sha256.Sum256([]byte(style))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
}()

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r, "GET, HEAD")
		return
	}
	w.Header().Set("Content-Security-Policy", publicHomeCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(publicHome)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, publicHome)
	}
}
