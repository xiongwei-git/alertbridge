package securestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const formatVersion byte = 1

type Cipher struct{ aead cipher.AEAD }

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	result := make([]byte, 1, 1+len(nonce)+len(plaintext)+c.aead.Overhead())
	result[0] = formatVersion
	result = append(result, nonce...)
	result = c.aead.Seal(result, nonce, plaintext, []byte{formatVersion})
	return result, nil
}

func (c *Cipher) Decrypt(value []byte) ([]byte, error) {
	minimum := 1 + c.aead.NonceSize() + c.aead.Overhead()
	if len(value) < minimum || value[0] != formatVersion {
		return nil, errors.New("unsupported or truncated encrypted value")
	}
	nonce := value[1 : 1+c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, value[1+c.aead.NonceSize():], []byte{formatVersion})
	if err != nil {
		return nil, errors.New("encrypted value authentication failed")
	}
	return plaintext, nil
}
