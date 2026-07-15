package passwordhash

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	version     = argon2.Version
	timeCost    = uint32(3)
	memoryCost  = uint32(64 * 1024)
	parallelism = uint8(1)
	saltLength  = 16
	keyLength   = uint32(32)
)

type parameters struct {
	time        uint32
	memory      uint32
	parallelism uint8
	salt        []byte
	hash        []byte
}

func Hash(password []byte) (string, error) {
	if len(password) == 0 {
		return "", errors.New("password is required")
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(password, salt, timeCost, memoryCost, parallelism, keyLength)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", version, memoryCost, timeCost, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
	clear(hash)
	debug.FreeOSMemory()
	return encoded, nil
}

func Validate(encoded string) error {
	_, err := parse(encoded)
	return err
}

func Verify(password []byte, encoded string) bool {
	params, err := parse(encoded)
	if err != nil {
		return false
	}
	actual := argon2.IDKey(password, params.salt, params.time, params.memory, params.parallelism, uint32(len(params.hash)))
	matched := subtle.ConstantTimeCompare(actual, params.hash) == 1
	clear(actual)
	debug.FreeOSMemory()
	return matched
}

func parse(encoded string) (parameters, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return parameters{}, errors.New("password hash must use Argon2id PHC format")
	}
	var parsedVersion int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &parsedVersion); err != nil || parsedVersion != version {
		return parameters{}, errors.New("unsupported Argon2id version")
	}
	var params parameters
	var threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &threads); err != nil {
		return parameters{}, errors.New("invalid Argon2id parameters")
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", params.memory, params.time, threads) {
		return parameters{}, errors.New("invalid Argon2id parameters")
	}
	if params.memory < 8*1024 || params.memory > 256*1024 || params.time < 1 || params.time > 10 || threads < 1 || threads > 8 {
		return parameters{}, errors.New("Argon2id parameters are outside safe bounds")
	}
	params.parallelism = uint8(threads)
	var err error
	params.salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(params.salt) < 16 || len(params.salt) > 64 {
		return parameters{}, errors.New("invalid Argon2id salt")
	}
	params.hash, err = base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(params.hash) < 16 || len(params.hash) > 64 {
		return parameters{}, errors.New("invalid Argon2id hash")
	}
	return params, nil
}
