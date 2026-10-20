package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const eventSubscriptionSchema = `
CREATE TABLE IF NOT EXISTS mcp_event_subscriptions (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL, policy TEXT NOT NULL, generation TEXT NOT NULL,
 routes TEXT NOT NULL, source TEXT NOT NULL, severity TEXT NOT NULL, event_status TEXT NOT NULL,
 encrypted BLOB NOT NULL, expires INTEGER NOT NULL, verified INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_event_expiry ON mcp_event_subscriptions(expires);
CREATE TABLE IF NOT EXISTS mcp_event_deliveries (
 delivery_id TEXT PRIMARY KEY REFERENCES deliveries(id) ON DELETE CASCADE,
 generation TEXT NOT NULL
);`

// Callback URLs and signing keys are stored only inside Encrypted.
type EventSubscription struct {
	ID, Owner, Policy, Routes, Source, Severity, Status, Generation string
	Encrypted                                                       []byte
	Expires, Verified                                               time.Time
}

func (s *Store) EventSubscription(ctx context.Context, id string) (EventSubscription, error) {
	var sub EventSubscription
	var expires, verified int64
	err := s.db.QueryRowContext(ctx, `SELECT id,owner,policy,generation,routes,source,severity,event_status,encrypted,expires,verified FROM mcp_event_subscriptions WHERE id=?`, id).Scan(&sub.ID, &sub.Owner, &sub.Policy, &sub.Generation, &sub.Routes, &sub.Source, &sub.Severity, &sub.Status, &sub.Encrypted, &expires, &verified)
	sub.Expires = time.UnixMilli(expires)
	sub.Verified = time.UnixMilli(verified)
	return sub, err
}
func (s *Store) SaveEventSubscription(ctx context.Context, sub EventSubscription, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Expired records can be removed; queued deliveries are checked again before sending.
	if _, err = tx.ExecContext(ctx, `DELETE FROM mcp_event_subscriptions WHERE expires<=?`, now.UnixMilli()); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mcp_event_subscriptions WHERE id<>?`, sub.ID).Scan(&count); err != nil {
		return err
	}
	if count >= 1000 {
		return errors.New("subscription limit reached")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mcp_event_subscriptions WHERE owner=? AND id<>?`, sub.Owner, sub.ID).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return errors.New("subscription limit reached")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_event_subscriptions(id,owner,policy,generation,routes,source,severity,event_status,encrypted,expires,verified) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET generation=excluded.generation,encrypted=excluded.encrypted,expires=excluded.expires,verified=excluded.verified`, sub.ID, sub.Owner, sub.Policy, sub.Generation, sub.Routes, sub.Source, sub.Severity, sub.Status, sub.Encrypted, sub.Expires.UnixMilli(), sub.Verified.UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RemoveEventSubscription(ctx context.Context, id, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_event_subscriptions WHERE id=? AND owner=?`, id, owner)
	return err
}
func (s *Store) OAuthGrantActive(ctx context.Context, id, policy string, now time.Time) bool {
	var found string
	return s.db.QueryRowContext(ctx, `SELECT id FROM mcp_oauth_grants WHERE id=? AND policy=? AND expires>?`, id, policy, now.UnixMilli()).Scan(&found) == nil
}

func enqueueEventSubscriptions(ctx context.Context, tx *sql.Tx, recordID string, p AcceptParams) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO deliveries(id,event_record_id,channel_id,status,next_attempt_at,created_at)
 SELECT ?||':'||id,?,'@mcp-event:'||id,'pending',?,? FROM mcp_event_subscriptions
 WHERE expires>? AND (owner NOT LIKE 'oauth:%' OR EXISTS (SELECT 1 FROM mcp_oauth_grants g WHERE g.id=substr(owner,7) AND g.policy=mcp_event_subscriptions.policy AND g.expires>?)) AND instr(','||routes||',',','||?||',')>0
 AND (source='' OR source=?) AND (severity='' OR severity=?) AND (event_status='' OR event_status=?)`, recordID, recordID, p.Now.UnixMilli(), p.Now.UnixMilli(), p.Now.UnixMilli(), p.Now.UnixMilli(), p.Event.RoutingKey, p.Event.Source, p.Event.Severity, p.Event.Status)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_event_deliveries(delivery_id,generation)
 SELECT d.id,s.generation FROM deliveries d JOIN mcp_event_subscriptions s ON d.channel_id='@mcp-event:'||s.id WHERE d.event_record_id=?`, recordID)
	return err
}

// PruneEventSubscriptions removes expired/revoked credentials; outstanding jobs fail closed.
func (s *Store) PruneEventSubscriptions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_event_subscriptions WHERE expires<=? OR
 (owner LIKE 'oauth:%' AND NOT EXISTS (SELECT 1 FROM mcp_oauth_grants g WHERE g.id=substr(owner,7) AND g.policy=mcp_event_subscriptions.policy AND g.expires>?))`, now.UnixMilli(), now.UnixMilli())
	return err
}
