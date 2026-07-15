package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

const maxFeishuPayload = 19 * 1024

type FeishuConfig struct {
	Webhook       string
	SigningSecret []byte
	MessageType   string
	Keyword       string
	Client        *http.Client
	Now           func() time.Time
}

type FeishuSender struct{ cfg FeishuConfig }

func NewFeishuSender(cfg FeishuConfig) *FeishuSender {
	cfg.Keyword = strings.TrimSpace(cfg.Keyword)
	if cfg.MessageType == "" {
		cfg.MessageType = "card"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		transport.MaxIdleConns = 10
		transport.MaxIdleConnsPerHost = 2
		cfg.Client = &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &FeishuSender{cfg: cfg}
}

func (s *FeishuSender) Send(ctx context.Context, event domain.Event) (int, error) {
	now := s.cfg.Now()
	var payload map[string]any
	if s.cfg.MessageType == "text" {
		payload = buildTextPayload(event, s.cfg.Keyword)
	} else {
		payload = buildCardPayload(event, now, s.cfg.Keyword)
	}
	if len(s.cfg.SigningSecret) > 0 {
		timestamp := fmt.Sprint(now.Unix())
		payload["timestamp"] = timestamp
		payload["sign"] = feishuSign(timestamp, s.cfg.SigningSecret)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, &SendError{Message: "encode Feishu payload", Retryable: false}
	}
	if len(body) > maxFeishuPayload {
		return 0, &SendError{Message: "Feishu payload exceeds safe size limit", Retryable: false}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Webhook, bytes.NewReader(body))
	if err != nil {
		return 0, &SendError{Message: "create Feishu request", Retryable: false}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AlertBridge/1")
	response, err := s.cfg.Client.Do(req)
	if err != nil {
		return 0, &SendError{Message: "Feishu request failed", Retryable: true}
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 32*1024))
	if readErr != nil {
		return response.StatusCode, &SendError{Message: "read Feishu response", StatusCode: response.StatusCode, Retryable: true}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == 408 || response.StatusCode == 425 || response.StatusCode == 429 || response.StatusCode >= 500
		return response.StatusCode, &SendError{Message: "Feishu returned an HTTP error", StatusCode: response.StatusCode, Retryable: retryable}
	}
	var result struct {
		Code       *int   `json:"code"`
		StatusCode *int   `json:"StatusCode"`
		Message    string `json:"msg"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return response.StatusCode, &SendError{Message: "Feishu returned an invalid response", StatusCode: response.StatusCode, Retryable: true}
	}
	if result.Code == nil && result.StatusCode == nil {
		return response.StatusCode, &SendError{Message: "Feishu returned an ambiguous response", StatusCode: response.StatusCode, Retryable: true}
	}
	if (result.Code != nil && *result.Code != 0) || (result.StatusCode != nil && *result.StatusCode != 0) {
		if (result.Code != nil && *result.Code == 19024) || (result.StatusCode != nil && *result.StatusCode == 19024) {
			return response.StatusCode, &SendError{Message: "Feishu keyword check failed", StatusCode: response.StatusCode, Retryable: false}
		}
		return response.StatusCode, &SendError{Message: "Feishu rejected the message", StatusCode: response.StatusCode, Retryable: true}
	}
	return response.StatusCode, nil
}

func buildTextPayload(event domain.Event, keyword string) map[string]any {
	return map[string]any{"msg_type": "text", "content": map[string]string{"text": withFeishuKeyword(renderPlainText(event), keyword)}}
}

func buildCardPayload(event domain.Event, now time.Time, keyword string) map[string]any {
	template := "blue"
	icon := "ℹ️"
	if event.Status == domain.StatusResolved {
		template, icon = "green", "✅"
	} else if event.Severity == domain.SeverityCritical {
		template, icon = "red", "🚨"
	} else if event.Severity == domain.SeverityWarning {
		template, icon = "orange", "⚠️"
	}
	lines := []string{
		fmt.Sprintf("**状态：** %s", escapeLarkMarkdown(string(event.Status))),
		fmt.Sprintf("**级别：** %s", escapeLarkMarkdown(string(event.Severity))),
		fmt.Sprintf("**来源：** %s", escapeLarkMarkdown(event.Source)),
		fmt.Sprintf("**时间：** %s", event.OccurredAt.Format(time.RFC3339)),
		"", escapeLarkMarkdown(event.Message),
	}
	if event.Status == domain.StatusResolved && event.IncidentStartedAt != nil {
		duration := now.Sub(*event.IncidentStartedAt)
		if duration < 0 {
			duration = 0
		}
		lines = append(lines, "", fmt.Sprintf("**故障持续：** %s", duration.Round(time.Second)))
	}
	keys := event.SortedLabelKeys()
	if len(keys) > 0 {
		lines = append(lines, "", "**标签：**")
		for _, key := range keys {
			lines = append(lines, fmt.Sprintf("- %s: %s", escapeLarkMarkdown(key), escapeLarkMarkdown(event.Labels[key])))
		}
	}
	elements := []any{map[string]any{"tag": "div", "text": map[string]string{"tag": "lark_md", "content": strings.Join(lines, "\n")}}}
	if event.URL != "" {
		elements = append(elements, map[string]any{
			"tag": "action",
			"actions": []any{map[string]any{
				"tag":  "button",
				"text": map[string]string{"tag": "plain_text", "content": "查看详情"},
				"url":  event.URL,
				"type": "primary",
			}},
		})
	}
	return map[string]any{"msg_type": "interactive", "card": map[string]any{
		"header":   map[string]any{"template": template, "title": map[string]string{"tag": "plain_text", "content": withFeishuKeyword(fmt.Sprintf("%s %s", icon, event.Title), keyword)}},
		"elements": elements,
	}}
}

func withFeishuKeyword(content, keyword string) string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return content
	}
	prefix := "【" + keyword + "】"
	if strings.HasPrefix(content, prefix) {
		return content
	}
	return prefix + content
}

func renderPlainText(event domain.Event) string {
	lines := []string{fmt.Sprintf("[%s/%s] %s", event.Status, event.Severity, event.Title), event.Message, "Source: " + event.Source, "Occurred: " + event.OccurredAt.Format(time.RFC3339)}
	keys := make([]string, 0, len(event.Labels))
	for key := range event.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, event.Labels[key]))
	}
	if event.URL != "" {
		lines = append(lines, event.URL)
	}
	return strings.Join(lines, "\n")
}

func feishuSign(timestamp string, secret []byte) string {
	stringToSign := timestamp + "\n" + string(secret)
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func escapeLarkMarkdown(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "#", "\\#")
	return replacer.Replace(value)
}
