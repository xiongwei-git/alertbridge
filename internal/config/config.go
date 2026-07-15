package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Dedupe   DedupeConfig
	Worker   WorkerConfig
	Admin    AdminConfig
	Clients  map[string]ClientConfig
	Channels map[string]ChannelConfig
	Routes   map[string]map[string][]string
}

type ServerConfig struct {
	Listen         string
	BodyLimitBytes int64
}

type DatabaseConfig struct {
	Path      string
	Retention time.Duration
}

type AuthConfig struct {
	TimestampTolerance time.Duration
	NonceRetention     time.Duration
}

type DedupeConfig struct{ Window time.Duration }

type WorkerConfig struct {
	PollInterval   time.Duration
	RequestTimeout time.Duration
	LeaseDuration  time.Duration
	RetryDelays    []time.Duration
	MaxAttempts    int
}

type AdminConfig struct {
	Enabled         bool
	Username        string
	Password        []byte
	MasterKey       []byte
	SessionLifetime time.Duration
	SecureCookie    bool
}

type ClientConfig struct {
	Enabled            bool
	Secret             []byte
	AllowedRoutes      []string
	RateLimitPerMinute int
}

type ChannelConfig struct {
	Type          string
	Enabled       bool
	Webhook       string
	SigningSecret []byte
	MessageType   string
	Keyword       string
	AllowedHosts  []string
}

type rawConfig struct {
	Server struct {
		Listen         string `json:"listen"`
		BodyLimitBytes int64  `json:"body_limit_bytes"`
	} `json:"server"`
	Database struct {
		Path      string `json:"path"`
		Retention string `json:"retention"`
	} `json:"database"`
	Auth struct {
		TimestampTolerance string `json:"timestamp_tolerance"`
		NonceRetention     string `json:"nonce_retention"`
	} `json:"auth"`
	Dedupe struct {
		Window string `json:"window"`
	} `json:"dedupe"`
	Worker struct {
		PollInterval   string   `json:"poll_interval"`
		RequestTimeout string   `json:"request_timeout"`
		LeaseDuration  string   `json:"lease_duration"`
		RetryDelays    []string `json:"retry_delays"`
		MaxAttempts    int      `json:"max_attempts"`
	} `json:"worker"`
	Admin struct {
		Enabled         bool   `json:"enabled"`
		Username        string `json:"username"`
		PasswordFile    string `json:"password_file"`
		MasterKeyFile   string `json:"master_key_file"`
		SessionLifetime string `json:"session_lifetime"`
		SecureCookie    *bool  `json:"secure_cookie"`
	} `json:"admin"`
	Clients map[string]struct {
		Enabled            bool     `json:"enabled"`
		SecretFile         string   `json:"secret_file"`
		AllowedRoutes      []string `json:"allowed_routes"`
		RateLimitPerMinute int      `json:"rate_limit_per_minute"`
	} `json:"clients"`
	Channels map[string]struct {
		Type              string   `json:"type"`
		Enabled           bool     `json:"enabled"`
		WebhookFile       string   `json:"webhook_file"`
		SigningSecretFile string   `json:"signing_secret_file,omitempty"`
		MessageType       string   `json:"message_type"`
		Keyword           string   `json:"keyword,omitempty"`
		AllowedHosts      []string `json:"allowed_hosts"`
		AllowInsecureHTTP bool     `json:"allow_insecure_http,omitempty"`
	} `json:"channels"`
	Routes map[string]map[string][]string `json:"routes"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var raw rawConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Config{}, err
	}
	baseDir := filepath.Dir(path)
	cfg := Config{
		Server:   ServerConfig{Listen: raw.Server.Listen, BodyLimitBytes: raw.Server.BodyLimitBytes},
		Database: DatabaseConfig{Path: resolvePath(baseDir, raw.Database.Path)},
		Clients:  make(map[string]ClientConfig, len(raw.Clients)),
		Channels: make(map[string]ChannelConfig, len(raw.Channels)), Routes: raw.Routes,
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":8080"
	}
	if cfg.Server.BodyLimitBytes == 0 {
		cfg.Server.BodyLimitBytes = 32 * 1024
	}
	if cfg.Server.BodyLimitBytes < 1024 || cfg.Server.BodyLimitBytes > 1024*1024 {
		return Config{}, fmt.Errorf("server.body_limit_bytes must be between 1024 and 1048576")
	}
	if cfg.Database.Path == "" {
		return Config{}, fmt.Errorf("database.path is required")
	}
	if cfg.Database.Retention, err = parseDuration("database.retention", raw.Database.Retention, 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Auth.TimestampTolerance, err = parseDuration("auth.timestamp_tolerance", raw.Auth.TimestampTolerance, 5*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Auth.NonceRetention, err = parseDuration("auth.nonce_retention", raw.Auth.NonceRetention, 10*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Auth.NonceRetention < cfg.Auth.TimestampTolerance {
		return Config{}, fmt.Errorf("auth.nonce_retention must be at least timestamp_tolerance")
	}
	if cfg.Dedupe.Window, err = parseDuration("dedupe.window", raw.Dedupe.Window, 30*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Worker.PollInterval, err = parseDuration("worker.poll_interval", raw.Worker.PollInterval, 500*time.Millisecond); err != nil {
		return Config{}, err
	}
	if cfg.Worker.RequestTimeout, err = parseDuration("worker.request_timeout", raw.Worker.RequestTimeout, 8*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.Worker.LeaseDuration, err = parseDuration("worker.lease_duration", raw.Worker.LeaseDuration, 30*time.Second); err != nil {
		return Config{}, err
	}
	if raw.Worker.MaxAttempts == 0 {
		raw.Worker.MaxAttempts = 6
	}
	if raw.Worker.MaxAttempts < 1 || raw.Worker.MaxAttempts > 20 {
		return Config{}, fmt.Errorf("worker.max_attempts must be between 1 and 20")
	}
	cfg.Worker.MaxAttempts = raw.Worker.MaxAttempts
	if len(raw.Worker.RetryDelays) == 0 {
		raw.Worker.RetryDelays = []string{"1s", "5s", "30s", "2m", "10m"}
	}
	for i, value := range raw.Worker.RetryDelays {
		d, parseErr := time.ParseDuration(value)
		if parseErr != nil || d <= 0 {
			return Config{}, fmt.Errorf("worker.retry_delays[%d] must be a positive duration", i)
		}
		cfg.Worker.RetryDelays = append(cfg.Worker.RetryDelays, d)
	}
	if len(cfg.Worker.RetryDelays) < cfg.Worker.MaxAttempts-1 {
		return Config{}, fmt.Errorf("worker.retry_delays needs at least max_attempts - 1 entries")
	}
	if raw.Admin.Enabled {
		cfg.Admin.Enabled = true
		cfg.Admin.Username = raw.Admin.Username
		if cfg.Admin.Username == "" {
			cfg.Admin.Username = "admin"
		}
		if !namePattern.MatchString(cfg.Admin.Username) || len(cfg.Admin.Username) > 64 {
			return Config{}, fmt.Errorf("admin.username must be a valid identifier with at most 64 bytes")
		}
		cfg.Admin.Password, err = readSecretFile(resolvePath(baseDir, raw.Admin.PasswordFile))
		if err != nil {
			return Config{}, fmt.Errorf("admin password: %w", err)
		}
		if len(cfg.Admin.Password) < 16 {
			return Config{}, fmt.Errorf("admin password must contain at least 16 bytes")
		}
		masterHex, readErr := readSecretFile(resolvePath(baseDir, raw.Admin.MasterKeyFile))
		if readErr != nil {
			return Config{}, fmt.Errorf("admin master key: %w", readErr)
		}
		cfg.Admin.MasterKey, err = hex.DecodeString(string(masterHex))
		if err != nil || len(cfg.Admin.MasterKey) != 32 {
			return Config{}, fmt.Errorf("admin master key must be 64 hexadecimal characters")
		}
		if cfg.Admin.SessionLifetime, err = parseDuration("admin.session_lifetime", raw.Admin.SessionLifetime, 12*time.Hour); err != nil {
			return Config{}, err
		}
		if cfg.Admin.SessionLifetime > 7*24*time.Hour {
			return Config{}, fmt.Errorf("admin.session_lifetime must not exceed 168h")
		}
		cfg.Admin.SecureCookie = true
		if raw.Admin.SecureCookie != nil {
			cfg.Admin.SecureCookie = *raw.Admin.SecureCookie
		}
		if !cfg.Admin.SecureCookie && os.Getenv("ALERTBRIDGE_ALLOW_INSECURE_ADMIN_COOKIE") != "1" {
			return Config{}, fmt.Errorf("admin secure_cookie=false requires ALERTBRIDGE_ALLOW_INSECURE_ADMIN_COOKIE=1")
		}
	}

	for id, item := range raw.Clients {
		if !namePattern.MatchString(id) {
			return Config{}, fmt.Errorf("invalid client id %q", id)
		}
		secret, readErr := readSecretFile(resolvePath(baseDir, item.SecretFile))
		if readErr != nil {
			return Config{}, fmt.Errorf("client %s: %w", id, readErr)
		}
		if len(secret) < 32 {
			return Config{}, fmt.Errorf("client %s: secret must contain at least 32 bytes", id)
		}
		if len(item.AllowedRoutes) == 0 {
			return Config{}, fmt.Errorf("client %s: allowed_routes is required", id)
		}
		if item.RateLimitPerMinute < 1 || item.RateLimitPerMinute > 600 {
			return Config{}, fmt.Errorf("client %s: rate_limit_per_minute must be between 1 and 600", id)
		}
		cfg.Clients[id] = ClientConfig{item.Enabled, secret, item.AllowedRoutes, item.RateLimitPerMinute}
	}
	if len(cfg.Clients) == 0 {
		return Config{}, fmt.Errorf("at least one client is required")
	}

	for id, item := range raw.Channels {
		if !namePattern.MatchString(id) {
			return Config{}, fmt.Errorf("invalid channel id %q", id)
		}
		if item.Type != "feishu" {
			return Config{}, fmt.Errorf("channel %s: unsupported type %q", id, item.Type)
		}
		webhookBytes, readErr := readSecretFile(resolvePath(baseDir, item.WebhookFile))
		if readErr != nil {
			return Config{}, fmt.Errorf("channel %s: %w", id, readErr)
		}
		if item.AllowInsecureHTTP && os.Getenv("ALERTBRIDGE_ALLOW_INSECURE_HTTP") != "1" {
			return Config{}, fmt.Errorf("channel %s: insecure HTTP requires ALERTBRIDGE_ALLOW_INSECURE_HTTP=1", id)
		}
		if err := validateWebhook(string(webhookBytes), item.AllowedHosts, item.AllowInsecureHTTP); err != nil {
			return Config{}, fmt.Errorf("channel %s: %w", id, err)
		}
		var signingSecret []byte
		if item.SigningSecretFile != "" {
			signingSecret, readErr = readSecretFile(resolvePath(baseDir, item.SigningSecretFile))
			if readErr != nil {
				return Config{}, fmt.Errorf("channel %s signing secret: %w", id, readErr)
			}
		}
		if item.MessageType == "" {
			item.MessageType = "card"
		}
		if item.MessageType != "card" && item.MessageType != "text" {
			return Config{}, fmt.Errorf("channel %s: message_type must be card or text", id)
		}
		keyword := strings.TrimSpace(item.Keyword)
		if strings.ContainsAny(keyword, "\r\n") || len([]rune(keyword)) > 64 {
			return Config{}, fmt.Errorf("channel %s: keyword must be at most 64 characters without line breaks", id)
		}
		cfg.Channels[id] = ChannelConfig{Type: item.Type, Enabled: item.Enabled, Webhook: string(webhookBytes), SigningSecret: signingSecret, MessageType: item.MessageType, Keyword: keyword, AllowedHosts: item.AllowedHosts}
	}
	if err := validateRoutes(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateRoutes(cfg Config) error {
	if len(cfg.Routes) == 0 {
		return fmt.Errorf("at least one route is required")
	}
	for route, severities := range cfg.Routes {
		if !namePattern.MatchString(route) {
			return fmt.Errorf("invalid route %q", route)
		}
		for severity, channels := range severities {
			if severity != "critical" && severity != "warning" && severity != "info" {
				return fmt.Errorf("route %s: invalid severity %q", route, severity)
			}
			if len(channels) == 0 {
				return fmt.Errorf("route %s severity %s needs at least one channel", route, severity)
			}
			for _, id := range channels {
				if _, ok := cfg.Channels[id]; !ok {
					return fmt.Errorf("route %s references unknown channel %s", route, id)
				}
			}
		}
	}
	for clientID, client := range cfg.Clients {
		for _, route := range client.AllowedRoutes {
			if _, ok := cfg.Routes[route]; !ok {
				return fmt.Errorf("client %s references unknown route %s", clientID, route)
			}
		}
	}
	return nil
}

func validateWebhook(raw string, allowedHosts []string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("webhook must be an absolute URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return fmt.Errorf("webhook must use HTTPS")
	}
	if len(allowedHosts) == 0 {
		return fmt.Errorf("allowed_hosts is required")
	}
	hostAllowed := false
	for _, host := range allowedHosts {
		if strings.EqualFold(u.Hostname(), host) {
			hostAllowed = true
			break
		}
	}
	if !hostAllowed {
		return fmt.Errorf("webhook host %q is not in allowed_hosts", u.Hostname())
	}
	return nil
}

func readSecretFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("secret file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat secret file: %w", err)
	}
	if info.Mode().Perm()&0o037 != 0 {
		return nil, fmt.Errorf("secret file %s permissions must allow at most group read and no access for others", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return nil, fmt.Errorf("secret file %s is empty", path)
	}
	return data, nil
}

func parseDuration(name, value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}

func resolvePath(baseDir, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("decode config: multiple JSON values are not allowed")
	}
	return fmt.Errorf("decode config: %w", err)
}
