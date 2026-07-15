package runtimecfg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/auth"
	"github.com/xiongwei-git/alertbridge/internal/channel"
	"github.com/xiongwei-git/alertbridge/internal/config"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

var (
	ErrConflict       = errors.New("record already exists")
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Options struct {
	Database          *store.Store
	Cipher            *securestore.Cipher
	Bootstrap         config.Config
	RequestTimeout    time.Duration
	AllowInsecureHTTP bool
	Logger            *slog.Logger
}

type Manager struct {
	database          *store.Store
	cipher            *securestore.Cipher
	requestTimeout    time.Duration
	allowInsecureHTTP bool
	logger            *slog.Logger
	state             atomic.Pointer[snapshot]
}

type snapshot struct {
	clients  map[string]auth.Client
	routes   map[string]map[string][]string
	senders  map[string]channel.Sender
	channels map[string]ChannelView
	silences []store.SilenceRecord
}

type storedChannelConfig struct {
	Webhook        string   `json:"webhook,omitempty"`
	SigningSecret  string   `json:"signing_secret,omitempty"`
	MessageType    string   `json:"message_type,omitempty"`
	Keyword        string   `json:"keyword,omitempty"`
	AllowedHosts   []string `json:"allowed_hosts,omitempty"`
	BotToken       string   `json:"bot_token,omitempty"`
	ChatID         string   `json:"chat_id,omitempty"`
	BaseURL        string   `json:"base_url,omitempty"`
	Endpoint       string   `json:"endpoint,omitempty"`
	Token          string   `json:"token,omitempty"`
	SMTPHost       string   `json:"smtp_host,omitempty"`
	SMTPPort       int      `json:"smtp_port,omitempty"`
	SMTPUsername   string   `json:"smtp_username,omitempty"`
	SMTPPassword   string   `json:"smtp_password,omitempty"`
	SMTPFrom       string   `json:"smtp_from,omitempty"`
	SMTPRecipients []string `json:"smtp_recipients,omitempty"`
	SMTPMode       string   `json:"smtp_mode,omitempty"`
}

type ClientView struct {
	ID                 string
	Enabled            bool
	AllowedRoutes      []string
	RateLimitPerMinute int
	UpdatedAt          time.Time
}

type ChannelView struct {
	ID         string
	Type       string
	Enabled    bool
	Summary    string
	HasSecret  bool
	HasKeyword bool
	UpdatedAt  time.Time
}

type ChannelInput struct {
	ID             string
	Type           string
	Enabled        bool
	Endpoint       string
	Secret         string
	ChatID         string
	SMTPHost       string
	SMTPPort       int
	SMTPUsername   string
	SMTPFrom       string
	SMTPRecipients []string
	SMTPMode       string
	MessageType    string
	Keyword        *string
}

type RouteView struct {
	RoutingKey string
	Severity   string
	Channels   []string
}

func New(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Database == nil || opts.Cipher == nil {
		return nil, errors.New("database and cipher are required")
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 8 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	manager := &Manager{database: opts.Database, cipher: opts.Cipher, requestTimeout: opts.RequestTimeout, allowInsecureHTTP: opts.AllowInsecureHTTP, logger: opts.Logger}
	if err := manager.seed(ctx, opts.Bootstrap); err != nil {
		return nil, err
	}
	if err := manager.Reload(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) seed(ctx context.Context, bootstrap config.Config) error {
	now := time.Now().UTC()
	clients := make([]store.ClientRecord, 0, len(bootstrap.Clients))
	for id, item := range bootstrap.Clients {
		secret, err := m.cipher.Encrypt(item.Secret)
		if err != nil {
			return err
		}
		clients = append(clients, store.ClientRecord{ID: id, Enabled: item.Enabled, SecretCipher: secret, AllowedRoutes: item.AllowedRoutes, RateLimitPerMinute: item.RateLimitPerMinute})
	}
	channels := make([]store.ChannelRecord, 0, len(bootstrap.Channels))
	for id, item := range bootstrap.Channels {
		stored := storedChannelConfig{Webhook: item.Webhook, SigningSecret: string(item.SigningSecret), MessageType: item.MessageType, Keyword: item.Keyword, AllowedHosts: item.AllowedHosts}
		ciphertext, err := m.encryptConfig(stored)
		if err != nil {
			return err
		}
		channels = append(channels, store.ChannelRecord{ID: id, Type: item.Type, Enabled: item.Enabled, ConfigCipher: ciphertext})
	}
	var routes []store.RouteRule
	for routingKey, severities := range bootstrap.Routes {
		for severity, channelIDs := range severities {
			for _, channelID := range channelIDs {
				routes = append(routes, store.RouteRule{RoutingKey: routingKey, Severity: severity, ChannelID: channelID})
			}
		}
	}
	_, err := m.database.SeedConfiguration(ctx, clients, channels, routes, now)
	return err
}

func (m *Manager) Reload(ctx context.Context) error {
	clientRecords, err := m.database.ListClients(ctx)
	if err != nil {
		return err
	}
	channelRecords, err := m.database.ListChannels(ctx)
	if err != nil {
		return err
	}
	rules, err := m.database.ListRoutes(ctx)
	if err != nil {
		return err
	}
	silences, err := m.database.ListSilences(ctx)
	if err != nil {
		return err
	}
	next := &snapshot{clients: make(map[string]auth.Client, len(clientRecords)), routes: map[string]map[string][]string{}, senders: make(map[string]channel.Sender), channels: make(map[string]ChannelView), silences: silences}
	for _, record := range clientRecords {
		secret, err := m.cipher.Decrypt(record.SecretCipher)
		if err != nil {
			return fmt.Errorf("decrypt client %s: %w", record.ID, err)
		}
		routes := make(map[string]struct{}, len(record.AllowedRoutes))
		for _, route := range record.AllowedRoutes {
			routes[route] = struct{}{}
		}
		next.clients[record.ID] = auth.Client{ID: record.ID, Secret: secret, Enabled: record.Enabled, AllowedRoutes: routes, RateLimitPerMin: record.RateLimitPerMinute}
	}
	for _, record := range channelRecords {
		stored, err := m.decryptConfig(record.ConfigCipher)
		if err != nil {
			return fmt.Errorf("decrypt channel %s: %w", record.ID, err)
		}
		sender, view, err := m.buildChannel(record, stored)
		if err != nil {
			return fmt.Errorf("channel %s: %w", record.ID, err)
		}
		next.channels[record.ID] = view
		if record.Enabled {
			next.senders[record.ID] = sender
		}
	}
	for _, rule := range rules {
		if next.routes[rule.RoutingKey] == nil {
			next.routes[rule.RoutingKey] = map[string][]string{}
		}
		next.routes[rule.RoutingKey][rule.Severity] = append(next.routes[rule.RoutingKey][rule.Severity], rule.ChannelID)
	}
	m.state.Store(next)
	return nil
}

func (m *Manager) LookupClient(id string) (auth.Client, bool) {
	state := m.state.Load()
	if state == nil {
		return auth.Client{}, false
	}
	client, ok := state.clients[id]
	return client, ok
}

func (m *Manager) ResolveTargets(route, severity string) []string {
	state := m.state.Load()
	if state == nil {
		return nil
	}
	configured := state.routes[route][severity]
	result := make([]string, 0, len(configured))
	for _, id := range configured {
		if _, ok := state.senders[id]; ok {
			result = append(result, id)
		}
	}
	return result
}

func (m *Manager) Sender(channelID string) (channel.Sender, bool) {
	state := m.state.Load()
	if state == nil {
		return nil, false
	}
	sender, ok := state.senders[channelID]
	return sender, ok
}

func (m *Manager) IsSilenced(route, severity string, now time.Time) bool {
	state := m.state.Load()
	if state == nil {
		return false
	}
	for _, silence := range state.silences {
		if now.Before(silence.StartsAt) || !now.Before(silence.EndsAt) {
			continue
		}
		if silence.RoutingKey != "*" && silence.RoutingKey != route {
			continue
		}
		if silence.Severity != "*" && silence.Severity != severity {
			continue
		}
		return true
	}
	return false
}

func (m *Manager) Clients() []ClientView {
	state := m.state.Load()
	if state == nil {
		return nil
	}
	result := make([]ClientView, 0, len(state.clients))
	records, err := m.database.ListClients(context.Background())
	if err != nil {
		return nil
	}
	for _, record := range records {
		result = append(result, ClientView{ID: record.ID, Enabled: record.Enabled, AllowedRoutes: append([]string(nil), record.AllowedRoutes...), RateLimitPerMinute: record.RateLimitPerMinute, UpdatedAt: record.UpdatedAt})
	}
	return result
}

func (m *Manager) Channels() []ChannelView {
	state := m.state.Load()
	if state == nil {
		return nil
	}
	result := make([]ChannelView, 0, len(state.channels))
	for _, view := range state.channels {
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (m *Manager) Routes() []RouteView {
	state := m.state.Load()
	if state == nil {
		return nil
	}
	var result []RouteView
	for route, severities := range state.routes {
		for severity, channels := range severities {
			result = append(result, RouteView{RoutingKey: route, Severity: severity, Channels: append([]string(nil), channels...)})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RoutingKey == result[j].RoutingKey {
			return result[i].Severity < result[j].Severity
		}
		return result[i].RoutingKey < result[j].RoutingKey
	})
	return result
}

func (m *Manager) CreateClient(ctx context.Context, id string, enabled bool, routes []string, limit int) (string, error) {
	if err := validateClient(id, routes, limit); err != nil {
		return "", err
	}
	if _, err := m.database.GetClient(ctx, id); err == nil {
		return "", ErrConflict
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	ciphertext, err := m.cipher.Encrypt([]byte(secret))
	if err != nil {
		return "", err
	}
	if err := m.database.UpsertClient(ctx, store.ClientRecord{ID: id, Enabled: enabled, SecretCipher: ciphertext, AllowedRoutes: routes, RateLimitPerMinute: limit}, time.Now().UTC()); err != nil {
		return "", err
	}
	if err := m.Reload(ctx); err != nil {
		return "", err
	}
	return secret, nil
}

func (m *Manager) UpdateClient(ctx context.Context, id string, enabled bool, routes []string, limit int) error {
	if err := validateClient(id, routes, limit); err != nil {
		return err
	}
	record, err := m.database.GetClient(ctx, id)
	if err != nil {
		return err
	}
	record.Enabled, record.AllowedRoutes, record.RateLimitPerMinute = enabled, routes, limit
	if err := m.database.UpsertClient(ctx, record, time.Now().UTC()); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) RotateClientSecret(ctx context.Context, id string) (string, error) {
	record, err := m.database.GetClient(ctx, id)
	if err != nil {
		return "", err
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	record.SecretCipher, err = m.cipher.Encrypt([]byte(secret))
	if err != nil {
		return "", err
	}
	if err := m.database.UpsertClient(ctx, record, time.Now().UTC()); err != nil {
		return "", err
	}
	if err := m.Reload(ctx); err != nil {
		return "", err
	}
	return secret, nil
}

func (m *Manager) DeleteClient(ctx context.Context, id string) error {
	if err := m.database.DeleteClient(ctx, id); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) UpsertChannel(ctx context.Context, input ChannelInput) error {
	if !identifierPattern.MatchString(input.ID) {
		return errors.New("invalid channel id")
	}
	stored := storedChannelConfig{}
	existing, err := m.database.GetChannel(ctx, input.ID)
	if err == nil {
		stored, err = m.decryptConfig(existing.ConfigCipher)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := mergeChannelInput(&stored, input, m.allowInsecureHTTP); err != nil {
		return err
	}
	ciphertext, err := m.encryptConfig(stored)
	if err != nil {
		return err
	}
	if err := m.database.UpsertChannel(ctx, store.ChannelRecord{ID: input.ID, Type: input.Type, Enabled: input.Enabled, ConfigCipher: ciphertext}, time.Now().UTC()); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) DeleteChannel(ctx context.Context, id string) error {
	if err := m.database.DeleteChannel(ctx, id); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) ReplaceRoute(ctx context.Context, route, severity string, channels []string) error {
	if !identifierPattern.MatchString(route) || !validSeverity(severity) {
		return errors.New("invalid route or severity")
	}
	if len(channels) == 0 {
		return errors.New("at least one channel is required")
	}
	state := m.state.Load()
	for _, id := range unique(channels) {
		if _, ok := state.channels[id]; !ok {
			return fmt.Errorf("unknown channel %s", id)
		}
	}
	if err := m.database.ReplaceRoute(ctx, route, severity, unique(channels), time.Now().UTC()); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) DeleteRoute(ctx context.Context, route, severity string) error {
	if err := m.database.DeleteRoute(ctx, route, severity); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) CreateSilence(ctx context.Context, route, severity string, starts, ends time.Time, reason string) error {
	if route != "*" && !identifierPattern.MatchString(route) {
		return errors.New("invalid silence route")
	}
	if severity != "*" && !validSeverity(severity) {
		return errors.New("invalid silence severity")
	}
	if !ends.After(starts) || ends.Sub(starts) > 30*24*time.Hour {
		return errors.New("silence must end after start and last at most 30 days")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 200 {
		return errors.New("silence reason must contain 1 to 200 characters")
	}
	if _, err := m.database.CreateSilence(ctx, store.SilenceRecord{RoutingKey: route, Severity: severity, StartsAt: starts.UTC(), EndsAt: ends.UTC(), Reason: reason}, time.Now().UTC()); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) DeleteSilence(ctx context.Context, id string) error {
	if err := m.database.DeleteSilence(ctx, id); err != nil {
		return err
	}
	return m.Reload(ctx)
}

func (m *Manager) QueueChannelTest(ctx context.Context, channelID string) error {
	if _, ok := m.Sender(channelID); !ok {
		return errors.New("channel is not enabled")
	}
	now := time.Now().UTC()
	event := domain.Event{EventID: "admin-test-" + strconv.FormatInt(now.UnixNano(), 10), Source: "admin", RoutingKey: "admin-test", Status: domain.StatusTest, Severity: domain.SeverityInfo, Title: "AlertBridge 渠道测试", Message: "这是一条由管理后台发起的测试通知。", OccurredAt: now}
	_, err := m.database.AcceptEvent(ctx, store.AcceptParams{ClientID: "admin", Event: event, Targets: []string{channelID}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{"source":"admin-test"}`)})
	return err
}

func (m *Manager) buildChannel(record store.ChannelRecord, cfg storedChannelConfig) (channel.Sender, ChannelView, error) {
	view := ChannelView{ID: record.ID, Type: record.Type, Enabled: record.Enabled, UpdatedAt: record.UpdatedAt}
	switch record.Type {
	case "feishu":
		if err := validateURL(cfg.Webhook, cfg.AllowedHosts, m.allowInsecureHTTP); err != nil {
			return nil, view, err
		}
		view.Summary, view.HasSecret, view.HasKeyword = hostSummary(cfg.Webhook), cfg.SigningSecret != "", cfg.Keyword != ""
		return channel.NewFeishuSender(channel.FeishuConfig{Webhook: cfg.Webhook, SigningSecret: []byte(cfg.SigningSecret), MessageType: cfg.MessageType, Keyword: cfg.Keyword, Client: channel.SecureHTTPClient(m.requestTimeout)}), view, nil
	case "telegram":
		if cfg.BotToken == "" || cfg.ChatID == "" {
			return nil, view, errors.New("bot token and chat id are required")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.telegram.org"
		}
		if !m.allowInsecureHTTP && cfg.BaseURL != "https://api.telegram.org" {
			return nil, view, errors.New("telegram base URL must be the official API")
		}
		view.Summary, view.HasSecret = "Chat "+cfg.ChatID, true
		return channel.NewTelegramSender(channel.TelegramConfig{BotToken: cfg.BotToken, ChatID: cfg.ChatID, BaseURL: cfg.BaseURL, Client: channel.SecureHTTPClient(m.requestTimeout)}), view, nil
	case "ntfy":
		if err := validateURL(cfg.Endpoint, nil, m.allowInsecureHTTP); err != nil {
			return nil, view, err
		}
		view.Summary, view.HasSecret = hostSummary(cfg.Endpoint), cfg.Token != ""
		return channel.NewNtfySender(channel.NtfyConfig{Endpoint: cfg.Endpoint, Token: cfg.Token, Client: channel.SecureHTTPClient(m.requestTimeout)}), view, nil
	case "smtp":
		if cfg.SMTPHost == "" || cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 || (cfg.SMTPMode != "tls" && cfg.SMTPMode != "starttls") {
			return nil, view, errors.New("invalid SMTP endpoint")
		}
		if _, err := mail.ParseAddress(cfg.SMTPFrom); err != nil {
			return nil, view, errors.New("invalid SMTP sender")
		}
		if len(cfg.SMTPRecipients) == 0 {
			return nil, view, errors.New("SMTP recipient is required")
		}
		for _, address := range cfg.SMTPRecipients {
			if _, err := mail.ParseAddress(address); err != nil {
				return nil, view, errors.New("invalid SMTP recipient")
			}
		}
		view.Summary, view.HasSecret = netJoin(cfg.SMTPHost, cfg.SMTPPort), cfg.SMTPPassword != ""
		return channel.NewSMTPSender(channel.SMTPConfig{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, Recipients: cfg.SMTPRecipients, Mode: cfg.SMTPMode, Timeout: m.requestTimeout}), view, nil
	default:
		return nil, view, fmt.Errorf("unsupported channel type %q", record.Type)
	}
}

func mergeChannelInput(cfg *storedChannelConfig, input ChannelInput, allowHTTP bool) error {
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	switch input.Type {
	case "feishu":
		if input.Endpoint != "" {
			cfg.Webhook = strings.TrimSpace(input.Endpoint)
		}
		if input.Secret != "" {
			cfg.SigningSecret = input.Secret
		}
		if input.MessageType != "" {
			cfg.MessageType = input.MessageType
		}
		if input.Keyword != nil {
			keyword := strings.TrimSpace(*input.Keyword)
			if strings.ContainsAny(keyword, "\r\n") || len([]rune(keyword)) > 64 {
				return errors.New("Feishu keyword must be at most 64 characters without line breaks")
			}
			cfg.Keyword = keyword
		}
		if cfg.MessageType == "" {
			cfg.MessageType = "card"
		}
		if cfg.MessageType != "card" && cfg.MessageType != "text" {
			return errors.New("feishu message type must be card or text")
		}
		u, err := url.Parse(cfg.Webhook)
		if err != nil || u.Hostname() == "" {
			return errors.New("invalid Feishu webhook")
		}
		if !allowHTTP && u.Hostname() != "open.feishu.cn" && u.Hostname() != "open.larksuite.com" {
			return errors.New("feishu webhook must use an official host")
		}
		cfg.AllowedHosts = []string{u.Hostname()}
		return validateURL(cfg.Webhook, cfg.AllowedHosts, allowHTTP)
	case "telegram":
		if input.Secret != "" {
			cfg.BotToken = input.Secret
		}
		if input.ChatID != "" {
			cfg.ChatID = strings.TrimSpace(input.ChatID)
		}
		if input.Endpoint != "" {
			cfg.BaseURL = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://api.telegram.org"
		}
		if cfg.BotToken == "" || cfg.ChatID == "" {
			return errors.New("telegram bot token and chat id are required")
		}
		if !allowHTTP && cfg.BaseURL != "https://api.telegram.org" {
			return errors.New("telegram endpoint must be https://api.telegram.org")
		}
		return nil
	case "ntfy":
		if input.Endpoint != "" {
			cfg.Endpoint = strings.TrimSpace(input.Endpoint)
		}
		if input.Secret != "" {
			cfg.Token = input.Secret
		}
		return validateURL(cfg.Endpoint, nil, allowHTTP)
	case "smtp":
		if input.SMTPHost != "" {
			cfg.SMTPHost = strings.TrimSpace(input.SMTPHost)
		}
		if input.SMTPPort != 0 {
			cfg.SMTPPort = input.SMTPPort
		}
		if input.SMTPUsername != "" {
			cfg.SMTPUsername = strings.TrimSpace(input.SMTPUsername)
		}
		if input.Secret != "" {
			cfg.SMTPPassword = input.Secret
		}
		if input.SMTPFrom != "" {
			cfg.SMTPFrom = strings.TrimSpace(input.SMTPFrom)
		}
		if len(input.SMTPRecipients) > 0 {
			cfg.SMTPRecipients = input.SMTPRecipients
		}
		if input.SMTPMode != "" {
			cfg.SMTPMode = input.SMTPMode
		}
		if cfg.SMTPHost == "" || cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 || (cfg.SMTPMode != "tls" && cfg.SMTPMode != "starttls") {
			return errors.New("SMTP host, port and TLS mode are required")
		}
		if _, err := mail.ParseAddress(cfg.SMTPFrom); err != nil {
			return errors.New("invalid SMTP sender")
		}
		if len(cfg.SMTPRecipients) == 0 {
			return errors.New("SMTP recipient is required")
		}
		return nil
	default:
		return errors.New("channel type must be feishu, telegram, ntfy, or smtp")
	}
}

func (m *Manager) encryptConfig(cfg storedChannelConfig) ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return m.cipher.Encrypt(data)
}
func (m *Manager) decryptConfig(value []byte) (storedChannelConfig, error) {
	data, err := m.cipher.Decrypt(value)
	if err != nil {
		return storedChannelConfig{}, err
	}
	var cfg storedChannelConfig
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

func validateClient(id string, routes []string, limit int) error {
	if !identifierPattern.MatchString(id) {
		return errors.New("invalid client id")
	}
	if len(routes) == 0 || len(routes) > 20 {
		return errors.New("client needs 1 to 20 routes")
	}
	for _, route := range routes {
		if !identifierPattern.MatchString(route) {
			return fmt.Errorf("invalid route %q", route)
		}
	}
	if limit < 1 || limit > 600 {
		return errors.New("rate limit must be between 1 and 600")
	}
	return nil
}

func validateURL(raw string, allowedHosts []string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("endpoint must be an absolute URL without credentials or fragment")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return errors.New("endpoint must use HTTPS")
	}
	if len(allowedHosts) > 0 {
		allowed := false
		for _, host := range allowedHosts {
			if strings.EqualFold(u.Hostname(), host) {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("endpoint host is not allowed")
		}
	}
	return nil
}

func validSeverity(value string) bool {
	return value == "critical" || value == "warning" || value == "info"
}
func unique(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
func newSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
func hostSummary(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "configured"
	}
	return u.Hostname()
}
func netJoin(host string, port int) string { return host + ":" + strconv.Itoa(port) }
