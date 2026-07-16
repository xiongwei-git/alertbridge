package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Dedupe   DedupeConfig
	Worker   WorkerConfig
	Admin    AdminConfig
	Display  DisplayConfig
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
	Username        string
	PasswordFile    string
	MasterKeyPath   string
	SessionLifetime time.Duration
	SecureCookie    bool
}

type DisplayConfig struct {
	TimeZone string
	Location *time.Location
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

func Load() (Config, error) {
	displayTimeZone := envOr("ALERTBRIDGE_DISPLAY_TIMEZONE", "Asia/Shanghai")
	displayLocation, err := time.LoadLocation(displayTimeZone)
	if err != nil {
		return Config{}, fmt.Errorf("load display timezone %q: %w", displayTimeZone, err)
	}
	cfg := Config{
		Server:   ServerConfig{Listen: envOr("ALERTBRIDGE_LISTEN", ":8080"), BodyLimitBytes: 32 * 1024},
		Database: DatabaseConfig{Path: envOr("ALERTBRIDGE_DATABASE_PATH", "/var/lib/alertbridge/alertbridge.db"), Retention: 30 * 24 * time.Hour},
		Auth:     AuthConfig{TimestampTolerance: 5 * time.Minute, NonceRetention: 10 * time.Minute},
		Dedupe:   DedupeConfig{Window: 30 * time.Minute},
		Worker: WorkerConfig{PollInterval: 500 * time.Millisecond, RequestTimeout: 8 * time.Second, LeaseDuration: 30 * time.Second,
			RetryDelays: []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}, MaxAttempts: 6},
		Admin: AdminConfig{
			Username:        envOr("ALERTBRIDGE_ADMIN_USERNAME", "admin"),
			PasswordFile:    envOr("ALERTBRIDGE_ADMIN_PASSWORD_FILE", "/run/secrets/admin_password"),
			MasterKeyPath:   envOr("ALERTBRIDGE_MASTER_KEY_PATH", "/var/lib/alertbridge-secrets/master.key"),
			SessionLifetime: 12 * time.Hour,
			SecureCookie:    os.Getenv("ALERTBRIDGE_ALLOW_INSECURE_ADMIN_COOKIE") != "1",
		},
		Display: DisplayConfig{TimeZone: displayTimeZone, Location: displayLocation},
	}
	if !usernamePattern.MatchString(cfg.Admin.Username) {
		return Config{}, errors.New("admin username must be a valid identifier with at most 64 bytes")
	}
	if !filepath.IsAbs(cfg.Database.Path) {
		return Config{}, errors.New("database path must be absolute")
	}
	if !filepath.IsAbs(cfg.Admin.MasterKeyPath) {
		return Config{}, errors.New("master key path must be absolute")
	}
	if !filepath.IsAbs(cfg.Admin.PasswordFile) {
		return Config{}, errors.New("admin password file path must be absolute")
	}
	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
