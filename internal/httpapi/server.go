package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/auth"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

type Config struct {
	Database        *store.Store
	Verifier        auth.Verifier
	Admin           http.Handler
	Routes          map[string]map[string][]string
	EnabledChannels map[string]bool
	ResolveTargets  func(string, string) []string
	IsSilenced      func(string, string, time.Time) bool
	NonceRetention  time.Duration
	DedupeWindow    time.Duration
	BodyLimitBytes  int64
	Now             func() time.Time
	Logger          *slog.Logger
}

type Server struct{ cfg Config }

type errorResponse struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type acceptedResponse struct {
	RequestID     string        `json:"request_id"`
	EventRecordID string        `json:"event_record_id"`
	EventID       string        `json:"event_id"`
	Outcome       store.Outcome `json:"outcome"`
	Reason        string        `json:"reason,omitempty"`
	Deliveries    int           `json:"deliveries"`
}

func New(cfg Config) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BodyLimitBytes <= 0 {
		cfg.BodyLimitBytes = 32 * 1024
	}
	server := &Server{cfg: cfg}
	if server.cfg.ResolveTargets == nil {
		server.cfg.ResolveTargets = server.staticTargets
	}
	if server.cfg.IsSilenced == nil {
		server.cfg.IsSilenced = func(string, string, time.Time) bool { return false }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/readyz", server.ready)
	mux.HandleFunc("/api/v1/events", server.events)
	mux.HandleFunc("/hooks/", server.legacyHookGone)
	if cfg.Admin != nil {
		mux.Handle("/admin", cfg.Admin)
		mux.Handle("/admin/", cfg.Admin)
	}
	return server.securityHeaders(mux)
}

func (s *Server) legacyHookGone(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusGone, "endpoint_removed", "this endpoint was removed; use POST /api/v1/events", requestID(r))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.cfg.Database.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "service is not ready", requestID(r))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := requestID(r)
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r, http.MethodPost)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", id)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.BodyLimitBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds the configured limit", id)
		return
	}
	headers := auth.Headers{ClientID: r.Header.Get("X-Notify-Client"), Timestamp: r.Header.Get("X-Notify-Timestamp"), Nonce: r.Header.Get("X-Notify-Nonce"), Signature: r.Header.Get("X-Notify-Signature")}
	client, err := s.cfg.Verifier.Verify(headers, body)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_failed", "request authentication failed", id)
		return
	}
	var event domain.Event
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not a valid event", id)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object", id)
		return
	}
	s.acceptEvent(w, r, id, client, headers, event, body)
}

func (s *Server) acceptEvent(w http.ResponseWriter, r *http.Request, id string, client auth.Client, headers auth.Headers, event domain.Event, body []byte) {
	now := s.cfg.Now().UTC()
	if err := event.Validate(now); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_event", err.Error(), id)
		return
	}
	if !client.CanUseRoute(event.RoutingKey) {
		writeError(w, http.StatusForbidden, "route_forbidden", "client is not allowed to use this route", id)
		return
	}
	targets := s.cfg.ResolveTargets(event.RoutingKey, string(event.Severity))
	if len(targets) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "route_unavailable", "route has no enabled channel for this severity", id)
		return
	}
	if err := s.cfg.Database.RecordRequest(r.Context(), client.ID, headers.Nonce, now.Add(s.cfg.NonceRetention), now, client.RateLimitPerMin); err != nil {
		switch {
		case errors.Is(err, store.ErrReplay):
			writeError(w, http.StatusUnauthorized, "authentication_failed", "request authentication failed", id)
		case errors.Is(err, store.ErrRateLimit):
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "client request rate exceeded", id)
		default:
			s.cfg.Logger.Error("record authentication state", "request_id", id, "client_id", client.ID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "service is temporarily unavailable", id)
		}
		return
	}
	suppressReason := ""
	if s.cfg.IsSilenced(event.RoutingKey, string(event.Severity), now) {
		suppressReason = "silence"
	}
	result, err := s.cfg.Database.AcceptEvent(r.Context(), store.AcceptParams{ClientID: client.ID, Event: event, Targets: targets, Now: now, DedupeWindow: s.cfg.DedupeWindow, SuppressReason: suppressReason, RawPayload: body})
	if err != nil {
		s.cfg.Logger.Error("accept event", "request_id", id, "client_id", client.ID, "event_id", event.EventID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "service is temporarily unavailable", id)
		return
	}
	s.cfg.Logger.Info("event accepted", "request_id", id, "client_id", client.ID, "event_id", event.EventID, "routing_key", event.RoutingKey, "outcome", result.Outcome, "deliveries", result.Deliveries)
	writeJSON(w, http.StatusAccepted, acceptedResponse{RequestID: id, EventRecordID: result.EventID, EventID: event.EventID, Outcome: result.Outcome, Reason: result.Reason, Deliveries: result.Deliveries})
}

func (s *Server) staticTargets(route, severity string) []string {
	severities, ok := s.cfg.Routes[route]
	if !ok {
		return nil
	}
	configured := severities[severity]
	result := make([]string, 0, len(configured))
	for _, id := range configured {
		if s.cfg.EnabledChannels[id] {
			result = append(result, id)
		}
	}
	return result
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func requestID(r *http.Request) string {
	if value := r.Context().Value(requestIDKey{}); value != nil {
		return value.(string)
	}
	value := r.Header.Get("X-Request-ID")
	if len(value) >= 8 && len(value) <= 64 {
		valid := true
		for _, char := range value {
			if !(char == '-' || char == '_' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	value, err := randomRequestID()
	if err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return value
}

type requestIDKey struct{}

func randomRequestID() (string, error) { return randomHex(12) }

func writeError(w http.ResponseWriter, status int, code, message, id string) {
	var body errorResponse
	body.Error.Code, body.Error.Message, body.Error.RequestID = code, message, id
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", requestID(r))
}
