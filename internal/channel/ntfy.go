package channel

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

type NtfyConfig struct {
	Endpoint string
	Token    string
	Client   *http.Client
}

type NtfySender struct{ cfg NtfyConfig }

func NewNtfySender(cfg NtfyConfig) *NtfySender {
	if cfg.Client == nil {
		cfg.Client = SecureHTTPClient(8 * time.Second)
	}
	return &NtfySender{cfg: cfg}
}

func (s *NtfySender) Send(ctx context.Context, event domain.Event) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewBufferString(renderPlainText(event)))
	if err != nil {
		return 0, &SendError{Message: "create ntfy request", Retryable: false}
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", safeHeader(event.Title))
	req.Header.Set("Priority", ntfyPriority(event.Severity))
	req.Header.Set("User-Agent", "AlertBridge/2")
	if s.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	}
	response, err := s.cfg.Client.Do(req)
	if err != nil {
		return 0, &SendError{Message: "ntfy request failed", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, &SendError{Message: "ntfy returned an HTTP error", StatusCode: response.StatusCode, Retryable: retryableHTTP(response.StatusCode)}
	}
	return response.StatusCode, nil
}

func ntfyPriority(severity domain.Severity) string {
	switch severity {
	case domain.SeverityCritical:
		return "urgent"
	case domain.SeverityWarning:
		return "high"
	default:
		return "default"
	}
}
