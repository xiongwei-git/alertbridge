package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

var (
	ErrUnknownClient  = errors.New("unknown client")
	ErrDisabledClient = errors.New("disabled client")
	ErrInvalidAuth    = errors.New("invalid authentication")
	ErrStaleRequest   = errors.New("stale request")
)

type Client struct {
	ID              string
	Secret          []byte
	Enabled         bool
	AllowedRoutes   map[string]struct{}
	RateLimitPerMin int
}

type Headers struct {
	ClientID  string
	Timestamp string
	Nonce     string
	Signature string
}

type Verifier struct {
	Clients   map[string]Client
	Lookup    func(string) (Client, bool)
	Tolerance time.Duration
	Now       func() time.Time
}

func (v Verifier) Verify(headers Headers, body []byte) (Client, error) {
	client, ok := Client{}, false
	if v.Lookup != nil {
		client, ok = v.Lookup(headers.ClientID)
	} else {
		client, ok = v.Clients[headers.ClientID]
	}
	if !ok {
		return Client{}, ErrUnknownClient
	}
	if !client.Enabled {
		return Client{}, ErrDisabledClient
	}
	timestamp, err := strconv.ParseInt(headers.Timestamp, 10, 64)
	if err != nil || timestamp <= 0 || !noncePattern.MatchString(headers.Nonce) {
		return Client{}, ErrInvalidAuth
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	requestTime := time.Unix(timestamp, 0)
	delta := now.Sub(requestTime)
	if delta < 0 {
		delta = -delta
	}
	if delta > v.Tolerance {
		return Client{}, ErrStaleRequest
	}
	want := Sign(client.Secret, headers.Timestamp, headers.Nonce, body)
	provided, err := hex.DecodeString(headers.Signature)
	if err != nil || len(provided) != sha256.Size || !hmac.Equal(provided, want) {
		return Client{}, ErrInvalidAuth
	}
	return client, nil
}

func Sign(secret []byte, timestamp, nonce string, body []byte) []byte {
	bodyDigest := sha256.Sum256(body)
	message := fmt.Sprintf("%s\n%s\n%s", timestamp, nonce, hex.EncodeToString(bodyDigest[:]))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

func SignHex(secret []byte, timestamp, nonce string, body []byte) string {
	return hex.EncodeToString(Sign(secret, timestamp, nonce, body))
}

func (c Client) CanUseRoute(route string) bool {
	_, ok := c.AllowedRoutes[route]
	return ok
}
