package credentials

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicGenerationReloadAndInvalidPair(t *testing.T) {
	root := t.TempDir()
	makePair := func(name string, serial int64) {
		dir := filepath.Join(root, name)
		os.Mkdir(dir, 0700)
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "client"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
		der, _ := x509.CreateCertificate(rand.Reader, c, c, &k.PublicKey, k)
		key, _ := x509.MarshalPKCS8PrivateKey(k)
		os.WriteFile(filepath.Join(dir, "client.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
		os.WriteFile(filepath.Join(dir, "client.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600)
	}
	makePair("one", 1)
	makePair("two", 2)
	os.Symlink("one", filepath.Join(root, "current"))
	first, err := Load(filepath.Join(root, "current/client.crt"), filepath.Join(root, "current/client.key"))
	if err != nil {
		t.Fatal(err)
	}
	os.Symlink("two", filepath.Join(root, "next"))
	os.Rename(filepath.Join(root, "next"), filepath.Join(root, "current"))
	second, err := Load(filepath.Join(root, "current/client.crt"), filepath.Join(root, "current/client.key"))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := x509.ParseCertificate(first.Certificate[0])
	b, _ := x509.ParseCertificate(second.Certificate[0])
	if a.SerialNumber.Cmp(b.SerialNumber) == 0 {
		t.Fatal("stale credential retained")
	}
	os.WriteFile(filepath.Join(root, "two/client.crt"), []byte("corrupt"), 0600)
	if _, err := Load(filepath.Join(root, "current/client.crt"), filepath.Join(root, "current/client.key")); err == nil {
		t.Fatal("invalid pair silently accepted")
	}
}
