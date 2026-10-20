// Package credentials loads atomic directory generations, preventing mixed key/cert reads.
package credentials

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func Load(certPath, keyPath string) (tls.Certificate, error) {
	if filepath.Dir(certPath) == filepath.Dir(keyPath) {
		dir, err := filepath.EvalSymlinks(filepath.Dir(certPath))
		if err != nil {
			return tls.Certificate{}, errors.New("credential directory unavailable")
		}
		certPath = filepath.Join(dir, filepath.Base(certPath))
		keyPath = filepath.Join(dir, filepath.Base(keyPath))
	}
	st, err := os.Stat(keyPath)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, errors.New("private key permissions invalid")
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return c, errors.New("certificate pair unavailable")
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("certificate not currently valid")
	}
	return c, nil
}
