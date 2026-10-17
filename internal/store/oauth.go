package store

import (
	"context"
	"errors"
	"time"
)

const oauthSchema = `
CREATE TABLE IF NOT EXISTS mcp_oauth_codes (
 hash TEXT PRIMARY KEY, challenge TEXT NOT NULL, redirect TEXT NOT NULL,
 resource TEXT NOT NULL, policy TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_oauth_grants (
 id TEXT PRIMARY KEY, access_hash TEXT NOT NULL UNIQUE, refresh_hash TEXT NOT NULL UNIQUE,
 policy TEXT NOT NULL, access_expires INTEGER NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_oauth_used_refresh (
 hash TEXT PRIMARY KEY, grant_id TEXT NOT NULL REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE
);`

var ErrOAuthGrant = errors.New("invalid grant")

func (s *Store) SaveOAuthCode(ctx context.Context, hash, challenge, redirect, resource, policy string, now time.Time) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_codes WHERE expires<=?`, now.UnixMilli()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE expires<=?`, now.UnixMilli()); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_codes(hash,challenge,redirect,resource,policy,expires) VALUES(?,?,?,?,?,?)`, hash, challenge, redirect, resource, policy, now.Add(5*time.Minute).UnixMilli())
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) ExchangeOAuthCode(ctx context.Context, hash, challenge, redirect, resource, policy, id, access, refresh string, now time.Time) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, `DELETE FROM mcp_oauth_codes WHERE hash=? AND challenge=? AND redirect=? AND resource=? AND policy=? AND expires>?`, hash, challenge, redirect, resource, policy, now.UnixMilli())
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrOAuthGrant
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_grants(id,access_hash,refresh_hash,policy,access_expires,expires) VALUES(?,?,?,?,?,?)`, id, access, refresh, policy, now.Add(time.Hour).UnixMilli(), now.Add(30*24*time.Hour).UnixMilli())
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) OAuthAccess(ctx context.Context, hash, policy string, now time.Time) (string, error) {
	var id string
	e := s.db.QueryRowContext(ctx, `SELECT id FROM mcp_oauth_grants WHERE access_hash=? AND policy=? AND access_expires>? AND expires>?`, hash, policy, now.UnixMilli(), now.UnixMilli()).Scan(&id)
	return id, e
}
func (s *Store) RotateOAuthRefresh(ctx context.Context, old, policy, access, refresh string, now time.Time) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Reusing any retired refresh token revokes its entire grant, including access.
	var reused string
	if e = tx.QueryRowContext(ctx, `SELECT grant_id FROM mcp_oauth_used_refresh WHERE hash=?`, old).Scan(&reused); e == nil {
		if _, e = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE id=?`, reused); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return ErrOAuthGrant
	}
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id FROM mcp_oauth_grants WHERE refresh_hash=? AND policy=? AND expires>?`, old, policy, now.UnixMilli()).Scan(&id); e != nil {
		return ErrOAuthGrant
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO mcp_oauth_used_refresh(hash,grant_id) VALUES(?,?)`, old, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE mcp_oauth_grants SET access_hash=?,refresh_hash=?,access_expires=? WHERE id=?`, access, refresh, now.Add(time.Hour).UnixMilli(), id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) RevokeOAuthToken(ctx context.Context, hash string) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_grants WHERE access_hash=? OR refresh_hash=? OR id IN (SELECT grant_id FROM mcp_oauth_used_refresh WHERE hash=?)`, hash, hash, hash)
	return e
}
func (s *Store) RevokeAllOAuth(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_codes`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM mcp_oauth_grants`); e != nil {
		return e
	}
	return tx.Commit()
}
