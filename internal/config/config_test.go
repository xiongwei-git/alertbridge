package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesSafeBuiltInDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8080" || cfg.Server.BodyLimitBytes != 32*1024 {
		t.Fatalf("server config = %+v", cfg.Server)
	}
	if cfg.Database.Path != "/var/lib/alertbridge/alertbridge.db" || cfg.Database.Retention != 30*24*time.Hour {
		t.Fatalf("database config = %+v", cfg.Database)
	}
	if cfg.Display.TimeZone != "Asia/Shanghai" || cfg.Display.Location.String() != "Asia/Shanghai" {
		t.Fatalf("display config = %+v", cfg.Display)
	}
	if cfg.Admin.Username != "admin" || cfg.Admin.PasswordFile != "/run/secrets/admin_password" || cfg.Admin.MasterKeyPath != "/var/lib/alertbridge-secrets/master.key" || !cfg.Admin.SecureCookie {
		t.Fatalf("admin config = %+v", cfg.Admin)
	}
}

func TestLoadAcceptsDeploymentOverrides(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ALERTBRIDGE_LISTEN", "127.0.0.1:9080")
	t.Setenv("ALERTBRIDGE_DATABASE_PATH", filepath.Join(t.TempDir(), "data.db"))
	t.Setenv("ALERTBRIDGE_MASTER_KEY_PATH", filepath.Join(t.TempDir(), "master.key"))
	t.Setenv("ALERTBRIDGE_ADMIN_USERNAME", "operator")
	t.Setenv("ALERTBRIDGE_ADMIN_PASSWORD_FILE", "/run/secrets/custom")
	t.Setenv("ALERTBRIDGE_DISPLAY_TIMEZONE", "UTC")
	t.Setenv("ALERTBRIDGE_ALLOW_INSECURE_ADMIN_COOKIE", "1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Username != "operator" || cfg.Admin.SecureCookie || cfg.Server.Listen != "127.0.0.1:9080" || cfg.Display.Location != time.UTC {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadRejectsInvalidDisplayTimezone(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ALERTBRIDGE_DISPLAY_TIMEZONE", "Mars/Olympus")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "display timezone") {
		t.Fatalf("Load() error = %v, want display timezone error", err)
	}
}

func TestLoadRejectsInvalidBootstrapIdentityOrPaths(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
		want  string
	}{
		{name: "username", env: "ALERTBRIDGE_ADMIN_USERNAME", value: "bad user", want: "username"},
		{name: "database path", env: "ALERTBRIDGE_DATABASE_PATH", value: "relative.db", want: "absolute"},
		{name: "key path", env: "ALERTBRIDGE_MASTER_KEY_PATH", value: "master.key", want: "absolute"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv(test.env, test.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ALERTBRIDGE_LISTEN", "ALERTBRIDGE_DATABASE_PATH", "ALERTBRIDGE_MASTER_KEY_PATH", "ALERTBRIDGE_ADMIN_USERNAME", "ALERTBRIDGE_ADMIN_PASSWORD_FILE", "ALERTBRIDGE_DISPLAY_TIMEZONE", "ALERTBRIDGE_ALLOW_INSECURE_ADMIN_COOKIE"} {
		t.Setenv(name, "")
	}
}
