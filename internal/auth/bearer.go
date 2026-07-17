package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrInvalidBearer = errors.New("invalid bearer token")

const bearerPrefix = "abt_"

type IngressToken struct {
	ID                 string
	PublicID           string
	SecretHash         []byte
	Enabled            bool
	RoutingKey         string
	Severity           string
	RateLimitPerMinute int
}

type BearerVerifier struct {
	Lookup func(string) (IngressToken, bool)
}

func GenerateBearerToken() (plain, publicID string, secretHash []byte, err error) {
	publicValue := make([]byte, 8)
	secret := make([]byte, 32)
	if _, err = rand.Read(publicValue); err != nil {
		return "", "", nil, err
	}
	if _, err = rand.Read(secret); err != nil {
		return "", "", nil, err
	}
	publicID = hex.EncodeToString(publicValue)
	digest := sha256.Sum256(secret)
	return bearerPrefix + publicID + "_" + hex.EncodeToString(secret), publicID, append([]byte(nil), digest[:]...), nil
}

func (v BearerVerifier) Verify(authorization string) (IngressToken, error) {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return IngressToken{}, ErrInvalidBearer
	}
	publicID, secret, ok := parseBearer(fields[1])
	if !ok {
		return IngressToken{}, ErrInvalidBearer
	}
	token, found := IngressToken{}, false
	if v.Lookup != nil {
		token, found = v.Lookup(publicID)
	}
	digest := sha256.Sum256(secret)
	expected := make([]byte, sha256.Size)
	if len(token.SecretHash) == sha256.Size {
		copy(expected, token.SecretHash)
	}
	matches := subtle.ConstantTimeCompare(digest[:], expected) == 1
	if !found || !token.Enabled || !matches {
		return IngressToken{}, ErrInvalidBearer
	}
	return token, nil
}

func parseBearer(value string) (string, []byte, bool) {
	if !strings.HasPrefix(value, bearerPrefix) {
		return "", nil, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, bearerPrefix), "_", 2)
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 64 {
		return "", nil, false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return "", nil, false
	}
	secret, err := hex.DecodeString(parts[1])
	if err != nil || len(secret) != 32 {
		return "", nil, false
	}
	return parts[0], secret, true
}
