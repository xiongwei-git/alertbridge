package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("record not found")
	ErrInvalidState = errors.New("invalid state transition")
)

const adminSchema = `
CREATE TABLE IF NOT EXISTS gateway_clients (
  id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  secret_cipher BLOB NOT NULL,
  allowed_routes_json TEXT NOT NULL,
  rate_limit_per_minute INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_channels (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  config_cipher BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS route_rules (
  routing_key TEXT NOT NULL,
  severity TEXT NOT NULL,
  channel_id TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (routing_key, severity, channel_id)
);
CREATE INDEX IF NOT EXISTS idx_route_rules_key ON route_rules(routing_key,severity);
CREATE TABLE IF NOT EXISTS silences (
  id TEXT PRIMARY KEY,
  routing_key TEXT NOT NULL,
  severity TEXT NOT NULL,
  starts_at INTEGER NOT NULL,
  ends_at INTEGER NOT NULL,
  reason TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_silences_active ON silences(starts_at,ends_at);
CREATE TABLE IF NOT EXISTS admin_sessions (
  token_hash BLOB PRIMARY KEY,
  csrf_token TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_admin_sessions_expiry ON admin_sessions(expires_at);
CREATE TABLE IF NOT EXISTS admin_login_windows (
  minute_bucket INTEGER PRIMARY KEY,
  attempt_count INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_credentials (
  singleton_id INTEGER PRIMARY KEY CHECK(singleton_id=1),
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
`

type ClientRecord struct {
	ID                 string
	Enabled            bool
	SecretCipher       []byte
	AllowedRoutes      []string
	RateLimitPerMinute int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ChannelRecord struct {
	ID           string
	Type         string
	Enabled      bool
	ConfigCipher []byte
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type RouteRule struct {
	RoutingKey string
	Severity   string
	ChannelID  string
}

type SilenceRecord struct {
	ID         string
	RoutingKey string
	Severity   string
	StartsAt   time.Time
	EndsAt     time.Time
	Reason     string
	CreatedAt  time.Time
}

type AdminSession struct {
	CSRFToken string
	ExpiresAt time.Time
}

type AdminCredential struct {
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type DashboardStats struct {
	Clients         int
	Channels        int
	ActiveIncidents int
	EventsToday     int
	Pending         int
	Retrying        int
	Dead            int
	Sent            int
}

type DeliveryView struct {
	ID           string
	EventID      string
	Source       string
	Title        string
	Severity     string
	Status       string
	ChannelID    string
	Attempts     int
	ResponseCode int
	LastError    string
	CreatedAt    time.Time
	SentAt       *time.Time
}

func (s *Store) InitializeAdminCredential(ctx context.Context, credential AdminCredential, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO admin_credentials(singleton_id,username,password_hash,created_at,updated_at) VALUES(1,?,?,?,?)`,
		credential.Username, credential.PasswordHash, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) GetAdminCredential(ctx context.Context) (AdminCredential, error) {
	var credential AdminCredential
	var createdMS, updatedMS int64
	err := s.db.QueryRowContext(ctx, `SELECT username,password_hash,created_at,updated_at FROM admin_credentials WHERE singleton_id=1`).Scan(
		&credential.Username, &credential.PasswordHash, &createdMS, &updatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminCredential{}, ErrNotFound
	}
	if err != nil {
		return AdminCredential{}, err
	}
	credential.CreatedAt, credential.UpdatedAt = time.UnixMilli(createdMS).UTC(), time.UnixMilli(updatedMS).UTC()
	return credential, nil
}

func (s *Store) UpsertClient(ctx context.Context, record ClientRecord, now time.Time) error {
	routes, err := json.Marshal(record.AllowedRoutes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_clients(id,enabled,secret_cipher,allowed_routes_json,rate_limit_per_minute,created_at,updated_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,secret_cipher=excluded.secret_cipher,allowed_routes_json=excluded.allowed_routes_json,rate_limit_per_minute=excluded.rate_limit_per_minute,updated_at=excluded.updated_at`,
		record.ID, boolInt(record.Enabled), record.SecretCipher, string(routes), record.RateLimitPerMinute, now.UnixMilli(), now.UnixMilli())
	return err
}

func (s *Store) GetClient(ctx context.Context, id string) (ClientRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,enabled,secret_cipher,allowed_routes_json,rate_limit_per_minute,created_at,updated_at FROM gateway_clients WHERE id=?`, id)
	return scanClient(row)
}

func (s *Store) ListClients(ctx context.Context) ([]ClientRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,enabled,secret_cipher,allowed_routes_json,rate_limit_per_minute,created_at,updated_at FROM gateway_clients ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClientRecord
	for rows.Next() {
		record, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func scanClient(scanner interface{ Scan(...any) error }) (ClientRecord, error) {
	var record ClientRecord
	var enabled int
	var routesJSON string
	var createdMS, updatedMS int64
	err := scanner.Scan(&record.ID, &enabled, &record.SecretCipher, &routesJSON, &record.RateLimitPerMinute, &createdMS, &updatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientRecord{}, ErrNotFound
	}
	if err != nil {
		return ClientRecord{}, err
	}
	if err := json.Unmarshal([]byte(routesJSON), &record.AllowedRoutes); err != nil {
		return ClientRecord{}, err
	}
	record.Enabled = enabled == 1
	record.CreatedAt, record.UpdatedAt = time.UnixMilli(createdMS).UTC(), time.UnixMilli(updatedMS).UTC()
	return record, nil
}

func (s *Store) DeleteClient(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM gateway_clients WHERE id=?", id)
	return requireAffected(result, err, ErrNotFound)
}

func (s *Store) UpsertChannel(ctx context.Context, record ChannelRecord, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_channels(id,type,enabled,config_cipher,created_at,updated_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET type=excluded.type,enabled=excluded.enabled,config_cipher=excluded.config_cipher,updated_at=excluded.updated_at`,
		record.ID, record.Type, boolInt(record.Enabled), record.ConfigCipher, now.UnixMilli(), now.UnixMilli())
	return err
}

func (s *Store) GetChannel(ctx context.Context, id string) (ChannelRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,type,enabled,config_cipher,created_at,updated_at FROM notification_channels WHERE id=?`, id)
	return scanChannel(row)
}

func (s *Store) ListChannels(ctx context.Context) ([]ChannelRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,type,enabled,config_cipher,created_at,updated_at FROM notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ChannelRecord
	for rows.Next() {
		record, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func scanChannel(scanner interface{ Scan(...any) error }) (ChannelRecord, error) {
	var record ChannelRecord
	var enabled int
	var createdMS, updatedMS int64
	err := scanner.Scan(&record.ID, &record.Type, &enabled, &record.ConfigCipher, &createdMS, &updatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelRecord{}, ErrNotFound
	}
	if err != nil {
		return ChannelRecord{}, err
	}
	record.Enabled = enabled == 1
	record.CreatedAt, record.UpdatedAt = time.UnixMilli(createdMS).UTC(), time.UnixMilli(updatedMS).UTC()
	return record, nil
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM notification_channels WHERE id=?", id)
	return requireAffected(result, err, ErrNotFound)
}

func (s *Store) ReplaceRoute(ctx context.Context, routingKey, severity string, channels []string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM route_rules WHERE routing_key=? AND severity=?", routingKey, severity); err != nil {
		return err
	}
	for _, channelID := range channels {
		if _, err := tx.ExecContext(ctx, "INSERT INTO route_rules(routing_key,severity,channel_id,created_at) VALUES(?,?,?,?)", routingKey, severity, channelID, now.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteRoute(ctx context.Context, routingKey, severity string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM route_rules WHERE routing_key=? AND severity=?", routingKey, severity)
	return requireAffected(result, err, ErrNotFound)
}

func (s *Store) ListRoutes(ctx context.Context) ([]RouteRule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT routing_key,severity,channel_id FROM route_rules ORDER BY routing_key,severity,channel_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RouteRule
	for rows.Next() {
		var rule RouteRule
		if err := rows.Scan(&rule.RoutingKey, &rule.Severity, &rule.ChannelID); err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Store) CreateSilence(ctx context.Context, record SilenceRecord, now time.Time) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO silences(id,routing_key,severity,starts_at,ends_at,reason,created_at) VALUES(?,?,?,?,?,?,?)`, id, record.RoutingKey, record.Severity, record.StartsAt.UnixMilli(), record.EndsAt.UnixMilli(), record.Reason, now.UnixMilli())
	return id, err
}

func (s *Store) DeleteSilence(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM silences WHERE id=?", id)
	return requireAffected(result, err, ErrNotFound)
}

func (s *Store) ListSilences(ctx context.Context) ([]SilenceRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,routing_key,severity,starts_at,ends_at,reason,created_at FROM silences ORDER BY ends_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SilenceRecord
	for rows.Next() {
		var record SilenceRecord
		var startsMS, endsMS, createdMS int64
		if err := rows.Scan(&record.ID, &record.RoutingKey, &record.Severity, &startsMS, &endsMS, &record.Reason, &createdMS); err != nil {
			return nil, err
		}
		record.StartsAt, record.EndsAt, record.CreatedAt = time.UnixMilli(startsMS).UTC(), time.UnixMilli(endsMS).UTC(), time.UnixMilli(createdMS).UTC()
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) CreateAdminSession(ctx context.Context, tokenHash []byte, csrfToken string, expiresAt, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE expires_at<?", now.UnixMilli())
	if _, err := tx.ExecContext(ctx, "INSERT INTO admin_sessions(token_hash,csrf_token,expires_at,created_at) VALUES(?,?,?,?)", tokenHash, csrfToken, expiresAt.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetAdminSession(ctx context.Context, tokenHash []byte, now time.Time) (AdminSession, error) {
	var session AdminSession
	var expiresMS int64
	err := s.db.QueryRowContext(ctx, "SELECT csrf_token,expires_at FROM admin_sessions WHERE token_hash=? AND expires_at>=?", tokenHash, now.UnixMilli()).Scan(&session.CSRFToken, &expiresMS)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, ErrNotFound
	}
	if err != nil {
		return AdminSession{}, err
	}
	session.ExpiresAt = time.UnixMilli(expiresMS).UTC()
	return session, nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash=?", tokenHash)
	return err
}

func (s *Store) RecordAdminLoginAttempt(ctx context.Context, now time.Time, limit int) error {
	bucket := now.Unix() / 60
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.ExecContext(ctx, "DELETE FROM admin_login_windows WHERE minute_bucket<?", bucket-2)
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_login_windows(minute_bucket,attempt_count) VALUES(?,1) ON CONFLICT(minute_bucket) DO UPDATE SET attempt_count=attempt_count+1`, bucket); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT attempt_count FROM admin_login_windows WHERE minute_bucket=?", bucket).Scan(&count); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if count > limit {
		return ErrRateLimit
	}
	return nil
}

func (s *Store) Dashboard(ctx context.Context, now time.Time) (DashboardStats, error) {
	var stats DashboardStats
	queries := []struct {
		query  string
		args   []any
		target *int
	}{
		{"SELECT COUNT(*) FROM gateway_clients", nil, &stats.Clients},
		{"SELECT COUNT(*) FROM notification_channels", nil, &stats.Channels},
		{"SELECT COUNT(*) FROM incidents WHERE state='firing'", nil, &stats.ActiveIncidents},
		{"SELECT COUNT(*) FROM events WHERE created_at>=?", []any{now.Truncate(24 * time.Hour).UnixMilli()}, &stats.EventsToday},
		{"SELECT COUNT(*) FROM deliveries WHERE status='pending'", nil, &stats.Pending},
		{"SELECT COUNT(*) FROM deliveries WHERE status='retrying'", nil, &stats.Retrying},
		{"SELECT COUNT(*) FROM deliveries WHERE status='dead'", nil, &stats.Dead},
		{"SELECT COUNT(*) FROM deliveries WHERE status='sent'", nil, &stats.Sent},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.target); err != nil {
			return DashboardStats{}, err
		}
	}
	return stats, nil
}

func (s *Store) ListDeliveries(ctx context.Context, status string, limit, offset int) ([]DeliveryView, int, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where, args := "", []any{}
	if status != "" {
		where, args = " WHERE d.status=?", append(args, status)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries d"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT d.id,e.event_id,e.source,e.title,e.severity,d.status,d.channel_id,d.attempts,d.response_code,d.last_error,d.created_at,d.sent_at
FROM deliveries d JOIN events e ON e.id=d.event_record_id` + where + ` ORDER BY d.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []DeliveryView
	for rows.Next() {
		var item DeliveryView
		var createdMS int64
		var sentMS sql.NullInt64
		if err := rows.Scan(&item.ID, &item.EventID, &item.Source, &item.Title, &item.Severity, &item.Status, &item.ChannelID, &item.Attempts, &item.ResponseCode, &item.LastError, &createdMS, &sentMS); err != nil {
			return nil, 0, err
		}
		item.CreatedAt = time.UnixMilli(createdMS).UTC()
		if sentMS.Valid {
			sent := time.UnixMilli(sentMS.Int64).UTC()
			item.SentAt = &sent
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

func (s *Store) RetryDeadDelivery(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET status='pending',attempts=0,next_attempt_at=?,lease_until=NULL,response_code=0,last_error='',sent_at=NULL WHERE id=? AND status='dead'`, now.UnixMilli(), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	var status string
	err = s.db.QueryRowContext(ctx, "SELECT status FROM deliveries WHERE id=?", id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: delivery status is %s", ErrInvalidState, status)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireAffected(result sql.Result, err error, empty error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return empty
	}
	return nil
}

func ValidateDeliveryStatus(status string) bool {
	switch strings.ToLower(status) {
	case "", "pending", "processing", "retrying", "sent", "dead":
		return true
	default:
		return false
	}
}
