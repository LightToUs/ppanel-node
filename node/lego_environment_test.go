package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCertificateEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certificate.env")
	if err := os.WriteFile(
		path,
		[]byte("CLOUDFLARE_DNS_API_TOKEN=test-token\n"),
		0600,
	); err != nil {
		t.Fatalf("write certificate environment: %v", err)
	}
	environment, err := loadCertificateEnvironment(path)
	if err != nil {
		t.Fatalf("load certificate environment: %v", err)
	}
	if environment["CLOUDFLARE_DNS_API_TOKEN"] != "test-token" {
		t.Fatalf("unexpected certificate environment: %#v", environment)
	}
}

func TestLoadCertificateEnvironmentRejectsOpenPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certificate.env")
	if err := os.WriteFile(path, []byte("VALID=value\n"), 0644); err != nil {
		t.Fatalf("write certificate environment: %v", err)
	}
	if _, err := loadCertificateEnvironment(path); err == nil {
		t.Fatal("loadCertificateEnvironment succeeded with open permissions")
	}
}

func TestWithCertificateEnvironmentRestoresProcessEnvironment(t *testing.T) {
	const key = "PPANEL_CERTIFICATE_ENVIRONMENT_TEST"
	t.Setenv(key, "before")
	err := withCertificateEnvironment(map[string]string{key: "during"}, func() error {
		if value := os.Getenv(key); value != "during" {
			t.Fatalf("environment during callback = %q", value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("with certificate environment: %v", err)
	}
	if value := os.Getenv(key); value != "before" {
		t.Fatalf("environment after callback = %q", value)
	}
}
