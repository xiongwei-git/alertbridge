package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	writeSecret(t, filepath.Join(dir, "client.secret"), strings.Repeat("a", 32))
	writeSecret(t, filepath.Join(dir, "webhook.secret"), "https://open.feishu.cn/open-apis/bot/v2/hook/example")
	path := filepath.Join(dir, "config.json")
	data := `{
		"server":{"listen":":8080","body_limit_bytes":32768},
		"database":{"path":"data/alertbridge.db","retention":"720h"},
		"auth":{"timestamp_tolerance":"5m","nonce_retention":"10m"},
		"dedupe":{"window":"30m"},
		"worker":{"poll_interval":"250ms","request_timeout":"8s","lease_duration":"30s","retry_delays":["1s","5s"],"max_attempts":3},
		"clients":{"gatus-us":{"enabled":true,"secret_file":"client.secret","allowed_routes":["infrastructure"],"rate_limit_per_minute":60}},
		"channels":{"feishu.ops":{"type":"feishu","enabled":true,"webhook_file":"webhook.secret","message_type":"card","keyword":"AlertBridge","allowed_hosts":["open.feishu.cn"]}},
		"routes":{"infrastructure":{"critical":["feishu.ops"],"warning":["feishu.ops"],"info":["feishu.ops"]}}
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := string(cfg.Clients["gatus-us"].Secret); got != strings.Repeat("a", 32) {
		t.Fatalf("client secret = %q", got)
	}
	if cfg.Database.Path != filepath.Join(dir, "data/alertbridge.db") {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.Database.Retention != 30*24*time.Hour {
		t.Fatalf("database retention = %s", cfg.Database.Retention)
	}
	if cfg.Worker.MaxAttempts != 3 || len(cfg.Worker.RetryDelays) != 2 {
		t.Fatalf("worker config = %+v", cfg.Worker)
	}
	if cfg.Channels["feishu.ops"].Keyword != "AlertBridge" {
		t.Fatalf("channel keyword = %q", cfg.Channels["feishu.ops"].Keyword)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want unknown field", err)
	}
}

func TestLoadRejectsWorldReadableSecret(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "client.secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("a", 32)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(secret); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("readSecretFile() error = %v, want permissions", err)
	}
}

func TestLoadAdminSecurityConfig(t *testing.T) {
	dir := t.TempDir()
	writeSecret(t, filepath.Join(dir, "client.secret"), strings.Repeat("a", 32))
	writeSecret(t, filepath.Join(dir, "webhook.secret"), "https://open.feishu.cn/open-apis/bot/v2/hook/example")
	writeSecret(t, filepath.Join(dir, "admin.password"), "long-local-admin-password")
	writeSecret(t, filepath.Join(dir, "master.key"), strings.Repeat("ab", 32))
	path := filepath.Join(dir, "config.json")
	data := `{
		"database":{"path":"data.db"},
		"admin":{"enabled":true,"username":"operator","password_file":"admin.password","master_key_file":"master.key","session_lifetime":"8h","secure_cookie":true},
		"clients":{"client":{"enabled":true,"secret_file":"client.secret","allowed_routes":["infra"],"rate_limit_per_minute":10}},
		"channels":{"feishu":{"type":"feishu","enabled":true,"webhook_file":"webhook.secret","message_type":"text","allowed_hosts":["open.feishu.cn"]}},
		"routes":{"infra":{"info":["feishu"]}}
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin.Enabled || cfg.Admin.Username != "operator" || len(cfg.Admin.MasterKey) != 32 || cfg.Admin.SessionLifetime != 8*time.Hour || !cfg.Admin.SecureCookie {
		t.Fatalf("admin config = %+v", cfg.Admin)
	}
}

func writeSecret(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
