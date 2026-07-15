package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiongwei-git/alertbridge/internal/passwordhash"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestInitializeCreatesCredentialAndPersistentMasterKey(t *testing.T) {
	dir := t.TempDir()
	database, err := store.Open(filepath.Join(dir, "data", "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	passwordPath := filepath.Join(dir, "admin-password")
	writeSecret(t, passwordPath, "correct horse battery staple")
	keyPath := filepath.Join(dir, "secrets", "master.key")

	result, err := Initialize(context.Background(), database, Options{Username: "admin", PasswordFile: passwordPath, MasterKeyPath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AdminCreated || !result.MasterKeyCreated || len(result.MasterKey) != 32 {
		t.Fatalf("Initialize() result = %+v", result)
	}
	if result.Credential.Username != "admin" || !passwordhash.Verify([]byte("correct horse battery staple"), result.Credential.PasswordHash) {
		t.Fatalf("credential = %+v", result.Credential)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode = %#o", info.Mode().Perm())
	}

	if err := os.Remove(passwordPath); err != nil {
		t.Fatal(err)
	}
	restarted, err := Initialize(context.Background(), database, Options{Username: "ignored", PasswordFile: passwordPath, MasterKeyPath: keyPath})
	if err != nil {
		t.Fatalf("restart Initialize() error = %v", err)
	}
	if restarted.AdminCreated || restarted.MasterKeyCreated || restarted.Credential.Username != "admin" {
		t.Fatalf("restart result = %+v", restarted)
	}
	if !bytes.Equal(restarted.MasterKey, result.MasterKey) {
		t.Fatal("master key changed after restart")
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), database, Options{Username: "admin", PasswordFile: passwordPath, MasterKeyPath: keyPath}); err == nil || !strings.Contains(err.Error(), "missing for an initialized database") {
		t.Fatalf("Initialize() after key loss error = %v", err)
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost master key was silently regenerated: %v", err)
	}
}

func TestInitializeRejectsWeakOrInsecureBootstrapPassword(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		data string
		want string
	}{
		{name: "weak", mode: 0o600, data: "too-short", want: "16 to 1024"},
		{name: "world-readable", mode: 0o644, data: "correct horse battery staple", want: "permissions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			database, err := store.Open(filepath.Join(dir, "alertbridge.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			path := filepath.Join(dir, "password")
			if err := os.WriteFile(path, []byte(test.data), test.mode); err != nil {
				t.Fatal(err)
			}
			_, err = Initialize(context.Background(), database, Options{Username: "admin", PasswordFile: path, MasterKeyPath: filepath.Join(dir, "keys", "master.key")})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Initialize() error = %v, want %q", err, test.want)
			}
			if _, getErr := database.GetAdminCredential(context.Background()); !errors.Is(getErr, store.ErrNotFound) {
				t.Fatalf("credential created after rejected bootstrap: %v", getErr)
			}
		})
	}
}

func TestLoadOrCreateMasterKeyRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	writeSecret(t, path, "not-a-valid-key")
	if _, _, err := loadOrCreateMasterKey(path, true); err == nil || !strings.Contains(err.Error(), "64 hexadecimal") {
		t.Fatalf("loadOrCreateMasterKey() error = %v", err)
	}
}

func writeSecret(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
