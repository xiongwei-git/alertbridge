package bootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/passwordhash"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type Options struct {
	Username      string
	PasswordFile  string
	MasterKeyPath string
}

type Result struct {
	Credential       store.AdminCredential
	MasterKey        []byte
	AdminCreated     bool
	MasterKeyCreated bool
}

func Initialize(ctx context.Context, database *store.Store, options Options) (Result, error) {
	if database == nil {
		return Result{}, errors.New("database is required")
	}
	credential, err := database.GetAdminCredential(ctx)
	adminExists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Result{}, fmt.Errorf("read admin credential: %w", err)
	}
	masterKey, keyCreated, err := loadOrCreateMasterKey(options.MasterKeyPath, !adminExists)
	if err != nil {
		return Result{}, fmt.Errorf("initialize master key: %w", err)
	}
	if adminExists {
		return Result{Credential: credential, MasterKey: masterKey, MasterKeyCreated: keyCreated}, nil
	}
	if !usernamePattern.MatchString(options.Username) {
		return Result{}, errors.New("admin username must be a valid identifier with at most 64 bytes")
	}
	password, err := readBootstrapPassword(options.PasswordFile)
	if err != nil {
		return Result{}, fmt.Errorf("read bootstrap admin password: %w", err)
	}
	defer clear(password)
	if len(password) < 16 || len(password) > 1024 {
		return Result{}, errors.New("bootstrap admin password must contain 16 to 1024 bytes")
	}
	if bytes.ContainsAny(password, "\r\n\x00") {
		return Result{}, errors.New("bootstrap admin password must not contain line breaks or NUL bytes")
	}
	encoded, err := passwordhash.Hash(password)
	if err != nil {
		return Result{}, fmt.Errorf("hash bootstrap admin password: %w", err)
	}
	created, err := database.InitializeAdminCredential(ctx, store.AdminCredential{Username: options.Username, PasswordHash: encoded}, time.Now().UTC())
	if err != nil {
		return Result{}, fmt.Errorf("initialize admin credential: %w", err)
	}
	credential, err = database.GetAdminCredential(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reload admin credential: %w", err)
	}
	return Result{Credential: credential, MasterKey: masterKey, AdminCreated: created, MasterKeyCreated: keyCreated}, nil
}

func readBootstrapPassword(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("password file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("password file must be a regular file")
	}
	// Docker Compose mounts secrets read-only and may expose mode 0444 inside the
	// isolated container. Ordinary host files must remain owner-only.
	if info.Mode().Perm()&0o077 != 0 && !isDockerSecretPath(path) {
		return nil, fmt.Errorf("password file permissions must allow owner access only: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSuffix(data, []byte("\r\n"))
	data = bytes.TrimSuffix(data, []byte("\n"))
	return data, nil
}

func isDockerSecretPath(path string) bool {
	clean := filepath.Clean(path)
	return clean == "/run/secrets" || strings.HasPrefix(clean, "/run/secrets/")
}

func loadOrCreateMasterKey(path string, allowCreate bool) ([]byte, bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, false, errors.New("master key path must be absolute")
	}
	if data, err := readMasterKey(path); err == nil {
		return data, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if !allowCreate {
		return nil, false, errors.New("master key is missing for an initialized database")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, false, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, false, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	defer clear(key)
	temporary, err := os.CreateTemp(directory, ".master-key-*")
	if err != nil {
		return nil, false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return nil, false, err
	}
	encoded := hex.EncodeToString(key) + "\n"
	if _, err := temporary.WriteString(encoded); err != nil {
		_ = temporary.Close()
		return nil, false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, false, err
	}
	if err := temporary.Close(); err != nil {
		return nil, false, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		existing, readErr := readMasterKey(path)
		return existing, false, readErr
	}
	return append([]byte(nil), key...), true, nil
}

func readMasterKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("master key must be a regular owner-only file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("master key must contain exactly 64 hexadecimal characters")
	}
	return decoded, nil
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
