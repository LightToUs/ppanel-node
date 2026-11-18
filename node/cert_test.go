package node

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
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
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat generated private key: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0600 {
		t.Fatalf("private key permissions = %o, want 600", permissions)
	}
}

func TestGenerateSelfSslCertificateSupportsIPAddressSNI(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "hysteria.cer")
	keyPath := filepath.Join(dir, "hysteria.key")
	const address = "203.0.113.10"

	if err := generateSelfSslCertificate(address, certPath, keyPath); err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	content, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read generated certificate: %v", err)
	}
	block, _ := pem.Decode(content)
	if block == nil {
		t.Fatal("generated certificate is not PEM encoded")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse generated certificate: %v", err)
	}
	if err = certificate.VerifyHostname(address); err != nil {
		t.Fatalf("verify generated IP certificate: %v", err)
	}
	if len(certificate.IPAddresses) != 1 || certificate.IPAddresses[0].String() != address {
		t.Fatalf("certificate IP SANs = %v, want [%s]", certificate.IPAddresses, address)
	}
	if len(certificate.DNSNames) != 0 {
		t.Fatalf("certificate DNS SANs = %v, want none", certificate.DNSNames)
	}
}
