package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

type TelegramConfig struct {
	BotToken string
	ChatID   string
	BaseURL  string
	Client   *http.Client
}

type TelegramSender struct{ cfg TelegramConfig }

func NewTelegramSender(cfg TelegramConfig) *TelegramSender {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.telegram.org"
	}
	if cfg.Client == nil {
		cfg.Client = SecureHTTPClient(8 * time.Second)
	}
	return &TelegramSender{cfg: cfg}
}

func (s *TelegramSender) Send(ctx context.Context, event domain.Event) (int, error) {
	payload := map[string]any{"chat_id": s.cfg.ChatID, "text": renderPlainText(event), "disable_web_page_preview": true}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, &SendError{Message: "encode Telegram payload", Retryable: false}
	}
	endpoint := strings.TrimRight(s.cfg.BaseURL, "/") + "/bot" + s.cfg.BotToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, &SendError{Message: "create Telegram request", Retryable: false}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AlertBridge/2")
	response, err := s.cfg.Client.Do(req)
	if err != nil {
		return 0, &SendError{Message: "Telegram request failed", Retryable: true}
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 32*1024))
	if readErr != nil {
		return response.StatusCode, &SendError{Message: "read Telegram response", StatusCode: response.StatusCode, Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, &SendError{Message: "Telegram returned an HTTP error", StatusCode: response.StatusCode, Retryable: retryableHTTP(response.StatusCode)}
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil || !result.OK {
		return response.StatusCode, &SendError{Message: "Telegram rejected the message", StatusCode: response.StatusCode, Retryable: true}
	}
	return response.StatusCode, nil
}
