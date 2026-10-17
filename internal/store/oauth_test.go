package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOAuthAtomicExchangePersistenceAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth.db")
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if e = db.SaveOAuthCode(ctx, "code-hash", "challenge", "redirect", "resource", "policy", now); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- db.ExchangeOAuthCode(ctx, "code-hash", "challenge", "redirect", "resource", "policy", id, "access-"+id, "refresh-"+id, now)
		}(id)
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if e != ErrOAuthGrant {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatal("code consumed more than once")
	}
	id := "one"
	if _, e = db.OAuthAccess(ctx, "access-one", "policy", now); e != nil {
		id = "two"
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.OAuthAccess(ctx, "access-"+id, "policy", now); e != nil {
		t.Fatal("grant lost after restart", e)
	}
	if _, e = db.OAuthAccess(ctx, "access-"+id, "changed-policy", now); e == nil {
		t.Fatal("policy change did not revoke")
	}
	if e = db.RotateOAuthRefresh(ctx, "refresh-"+id, "policy", "new-access", "new-refresh", now.Add(31*24*time.Hour)); e != ErrOAuthGrant {
		t.Fatal("expired refresh accepted", e)
	}
}
