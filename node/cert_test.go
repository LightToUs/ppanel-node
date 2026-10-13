package node

import (
	"crypto/tls"
	"path/filepath"
	"testing"
)

func TestGenerateSelfSslCertificateProducesLoadableKeyPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "hysteria.cer")
	keyPath := filepath.Join(dir, "hysteria.key")

	if err := generateSelfSslCertificate("hy2.example.com", certPath, keyPath); err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("load generated key pair: %v", err)
	}
}
