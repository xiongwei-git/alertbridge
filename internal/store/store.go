package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/domain"
	_ "modernc.org/sqlite"
)

var (
	ErrReplay    = errors.New("nonce replay")
	ErrRateLimit = errors.New("rate limit exceeded")
)

type Outcome string

const (
	OutcomeQueued     Outcome = "queued"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeDuplicate  Outcome = "duplicate"
)

type Store struct{ db *sql.DB }

type AcceptParams struct {
	ClientID       string
	Event          domain.Event
	Targets        []string
	Now            time.Time
	DedupeWindow   time.Duration
	SuppressReason string
	RawPayload     []byte
}

type AcceptResult struct {
	EventID           string     `json:"request_id"`
	Outcome           Outcome    `json:"outcome"`
	Reason            string     `json:"reason,omitempty"`
	Deliveries        int        `json:"deliveries"`
	IncidentStartedAt *time.Time `json:"-"`
}

type Delivery struct {
	ID        string
	ChannelID string
	Attempts  int
	Event     domain.Event
}

type DeliveryStatus struct {
	Status   string
	Attempts int
}

const schema = `
CREATE TABLE IF NOT EXISTS nonces (
  client_id TEXT NOT NULL,
  nonce TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (client_id, nonce)
);
CREATE INDEX IF NOT EXISTS idx_nonces_expiry ON nonces(expires_at);
CREATE TABLE IF NOT EXISTS rate_windows (
  client_id TEXT NOT NULL,
  minute_bucket INTEGER NOT NULL,
  request_count INTEGER NOT NULL,
  PRIMARY KEY (client_id, minute_bucket)
);
CREATE TABLE IF NOT EXISTS incidents (
  client_id TEXT NOT NULL,
  routing_key TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('firing','resolved')),
  first_fired_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  last_notified_at INTEGER NOT NULL,
  PRIMARY KEY (client_id, routing_key, dedupe_key)
);
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  source TEXT NOT NULL,
  routing_key TEXT NOT NULL,
  status TEXT NOT NULL,
  severity TEXT NOT NULL,
  title TEXT NOT NULL,
  message TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  dedupe_key TEXT NOT NULL,
  labels_json TEXT NOT NULL,
  url TEXT NOT NULL,
  raw_payload BLOB NOT NULL,
  outcome TEXT NOT NULL,
  incident_started_at INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE (client_id, event_id)
);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
CREATE TABLE IF NOT EXISTS deliveries (
  id TEXT PRIMARY KEY,
  event_record_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('pending','processing','retrying','sent','dead')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  lease_until INTEGER,
  response_code INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  sent_at INTEGER,
  UNIQUE(event_record_id, channel_id)
);
CREATE INDEX IF NOT EXISTS idx_deliveries_due ON deliveries(status, next_attempt_at, lease_until);
`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pragmas := []string{
		"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000", "PRAGMA trusted_schema=OFF", "PRAGMA secure_delete=ON",
	}
	for _, statement := range pragmas {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if _, err := db.Exec(schema + adminSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		_ = db.Close()
		if err != nil {
			return nil, fmt.Errorf("check sqlite integrity: %w", err)
		}
		return nil, fmt.Errorf("check sqlite integrity: %s", integrity)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure database permissions: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) RecordRequest(ctx context.Context, clientID, nonce string, expiresAt, now time.Time, limit int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.ExecContext(ctx, "DELETE FROM nonces WHERE expires_at < ?", now.UnixMilli())
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO nonces(client_id, nonce, expires_at) VALUES(?,?,?)", clientID, nonce, expiresAt.UnixMilli())
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return ErrReplay
	}
	exceeded, err := recordRateWindow(ctx, tx, "hmac:"+clientID, now, limit)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if exceeded {
		return ErrRateLimit
	}
	return nil
}

func (s *Store) RecordRateLimit(ctx context.Context, key string, now time.Time, limit int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	exceeded, err := recordRateWindow(ctx, tx, key, now, limit)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if exceeded {
		return ErrRateLimit
	}
	return nil
}

func recordRateWindow(ctx context.Context, tx *sql.Tx, key string, now time.Time, limit int) (bool, error) {
	bucket := now.Unix() / 60
	_, _ = tx.ExecContext(ctx, "DELETE FROM rate_windows WHERE minute_bucket < ?", bucket-2)
	var count int
	err := tx.QueryRowContext(ctx, "SELECT request_count FROM rate_windows WHERE client_id=? AND minute_bucket=?", key, bucket).Scan(&count)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if limit < 1 {
			return true, nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO rate_windows(client_id, minute_bucket, request_count) VALUES(?,?,1)", key, bucket); err != nil {
			return false, err
		}
		return false, nil
	case err != nil:
		return false, err
	case count >= limit:
		return true, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE rate_windows SET request_count=request_count+1 WHERE client_id=? AND minute_bucket=?", key, bucket); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) AcceptEvent(ctx context.Context, p AcceptParams) (AcceptResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AcceptResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID, existingOutcome string
	var existingDeliveries int
	err = tx.QueryRowContext(ctx, `SELECT e.id, e.outcome, COUNT(d.id) FROM events e LEFT JOIN deliveries d ON d.event_record_id=e.id
WHERE e.client_id=? AND e.event_id=? GROUP BY e.id`, p.ClientID, p.Event.EventID).Scan(&existingID, &existingOutcome, &existingDeliveries)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return AcceptResult{}, err
		}
		return AcceptResult{EventID: existingID, Outcome: OutcomeDuplicate, Deliveries: existingDeliveries}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AcceptResult{}, err
	}

	outcome, reason := OutcomeQueued, ""
	var incidentStarted *time.Time
	if p.Event.Status == domain.StatusFiring || p.Event.Status == domain.StatusResolved {
		var state string
		var firstFiredMS, lastNotifiedMS int64
		err = tx.QueryRowContext(ctx, `SELECT state, first_fired_at, last_notified_at FROM incidents
WHERE client_id=? AND routing_key=? AND dedupe_key=?`, p.ClientID, p.Event.RoutingKey, p.Event.DedupeKey).Scan(&state, &firstFiredMS, &lastNotifiedMS)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AcceptResult{}, err
		}
		nowMS := p.Now.UnixMilli()
		if p.Event.Status == domain.StatusFiring {
			switch {
			case !exists || state == string(domain.StatusResolved):
				_, err = tx.ExecContext(ctx, `INSERT INTO incidents(client_id,routing_key,dedupe_key,state,first_fired_at,last_seen_at,last_notified_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(client_id,routing_key,dedupe_key) DO UPDATE SET state=excluded.state,first_fired_at=excluded.first_fired_at,last_seen_at=excluded.last_seen_at,last_notified_at=excluded.last_notified_at`,
					p.ClientID, p.Event.RoutingKey, p.Event.DedupeKey, domain.StatusFiring, nowMS, nowMS, nowMS)
			case p.Now.Sub(time.UnixMilli(lastNotifiedMS)) < p.DedupeWindow:
				outcome, reason = OutcomeSuppressed, "dedupe_window"
				_, err = tx.ExecContext(ctx, `UPDATE incidents SET last_seen_at=? WHERE client_id=? AND routing_key=? AND dedupe_key=?`, nowMS, p.ClientID, p.Event.RoutingKey, p.Event.DedupeKey)
			default:
				_, err = tx.ExecContext(ctx, `UPDATE incidents SET last_seen_at=?,last_notified_at=? WHERE client_id=? AND routing_key=? AND dedupe_key=?`, nowMS, nowMS, p.ClientID, p.Event.RoutingKey, p.Event.DedupeKey)
			}
		} else if !exists || state != string(domain.StatusFiring) {
			outcome, reason = OutcomeSuppressed, "orphan_resolved"
		} else {
			started := time.UnixMilli(firstFiredMS).UTC()
			incidentStarted = &started
			_, err = tx.ExecContext(ctx, `UPDATE incidents SET state=?,last_seen_at=?,last_notified_at=? WHERE client_id=? AND routing_key=? AND dedupe_key=?`, domain.StatusResolved, nowMS, nowMS, p.ClientID, p.Event.RoutingKey, p.Event.DedupeKey)
		}
		if err != nil {
			return AcceptResult{}, err
		}
	}
	if p.SuppressReason != "" && outcome == OutcomeQueued {
		outcome, reason = OutcomeSuppressed, p.SuppressReason
	}

	recordID, err := newID()
	if err != nil {
		return AcceptResult{}, err
	}
	labels, err := json.Marshal(p.Event.Labels)
	if err != nil {
		return AcceptResult{}, err
	}
	var incidentMS any
	if incidentStarted != nil {
		incidentMS = incidentStarted.UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id,client_id,event_id,source,routing_key,status,severity,title,message,occurred_at,dedupe_key,labels_json,url,raw_payload,outcome,incident_started_at,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, recordID, p.ClientID, p.Event.EventID, p.Event.Source, p.Event.RoutingKey, p.Event.Status, p.Event.Severity,
		p.Event.Title, p.Event.Message, p.Event.OccurredAt.UnixMilli(), p.Event.DedupeKey, string(labels), p.Event.URL, p.RawPayload, outcome, incidentMS, p.Now.UnixMilli())
	if err != nil {
		return AcceptResult{}, err
	}
	deliveries := 0
	if outcome == OutcomeQueued {
		for _, target := range uniqueStrings(p.Targets) {
			deliveryID, idErr := newID()
			if idErr != nil {
				return AcceptResult{}, idErr
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries(id,event_record_id,channel_id,status,next_attempt_at,created_at) VALUES(?,?,?,'pending',?,?)`, deliveryID, recordID, target, p.Now.UnixMilli(), p.Now.UnixMilli()); err != nil {
				return AcceptResult{}, err
			}
			deliveries++
		}
	}
	if err := tx.Commit(); err != nil {
		return AcceptResult{}, err
	}
	return AcceptResult{EventID: recordID, Outcome: outcome, Reason: reason, Deliveries: deliveries, IncidentStartedAt: incidentStarted}, nil
}

func (s *Store) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (*Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT d.id,d.channel_id,d.attempts,e.event_id,e.source,e.routing_key,e.status,e.severity,e.title,e.message,e.occurred_at,e.dedupe_key,e.labels_json,e.url,e.incident_started_at
FROM deliveries d JOIN events e ON e.id=d.event_record_id
WHERE ((d.status IN ('pending','retrying') AND d.next_attempt_at<=?) OR (d.status='processing' AND d.lease_until<=?))
ORDER BY d.next_attempt_at,d.created_at LIMIT 1`, now.UnixMilli(), now.UnixMilli())
	var d Delivery
	var status, severity, labelsJSON string
	var occurredMS int64
	var incidentMS sql.NullInt64
	if err := row.Scan(&d.ID, &d.ChannelID, &d.Attempts, &d.Event.EventID, &d.Event.Source, &d.Event.RoutingKey, &status, &severity,
		&d.Event.Title, &d.Event.Message, &occurredMS, &d.Event.DedupeKey, &labelsJSON, &d.Event.URL, &incidentMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='processing',attempts=attempts+1,lease_until=? WHERE id=? AND ((status IN ('pending','retrying') AND next_attempt_at<=?) OR (status='processing' AND lease_until<=?))`, now.Add(lease).UnixMilli(), d.ID, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		if err != nil {
			return nil, err
		}
		return nil, nil
	}
	d.Attempts++
	d.Event.Status, d.Event.Severity = domain.Status(status), domain.Severity(severity)
	d.Event.OccurredAt = time.UnixMilli(occurredMS).UTC()
	if err := json.Unmarshal([]byte(labelsJSON), &d.Event.Labels); err != nil {
		return nil, err
	}
	if incidentMS.Valid {
		started := time.UnixMilli(incidentMS.Int64).UTC()
		d.Event.IncidentStartedAt = &started
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) CompleteSuccess(ctx context.Context, deliveryID string, responseCode int, sentAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET status='sent',lease_until=NULL,response_code=?,last_error='',sent_at=? WHERE id=? AND status='processing'`, responseCode, sentAt.UnixMilli(), deliveryID)
	return requireOne(result, err)
}

func (s *Store) CompleteFailure(ctx context.Context, deliveryID string, attempts int, next time.Time, message string, responseCode int, dead bool) error {
	status := "retrying"
	if dead {
		status = "dead"
	}
	if len(message) > 500 {
		message = message[:500]
	}
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET status=?,lease_until=NULL,next_attempt_at=?,response_code=?,last_error=? WHERE id=? AND status='processing' AND attempts=?`, status, next.UnixMilli(), responseCode, message, deliveryID, attempts)
	return requireOne(result, err)
}

func (s *Store) GetDeliveryStatus(ctx context.Context, eventRecordID, channelID string) (DeliveryStatus, error) {
	var result DeliveryStatus
	err := s.db.QueryRowContext(ctx, "SELECT status,attempts FROM deliveries WHERE event_record_id=? AND channel_id=?", eventRecordID, channelID).Scan(&result.Status, &result.Attempts)
	return result, err
}

func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at < ? AND NOT EXISTS (
SELECT 1 FROM deliveries d WHERE d.event_record_id=events.id AND d.status IN ('pending','processing','retrying'))`, before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("delivery state transition affected %d rows", rows)
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func newID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buffer), nil
}
