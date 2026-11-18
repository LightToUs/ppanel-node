package node

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lighttous/ppanel-node/api/panel"
	"github.com/lighttous/ppanel-node/common/file"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) renewCertTask(_ context.Context) error {
	l, err := NewLego(c.info)
	if err != nil {
		log.WithField("节点", c.tag).Info("new lego error: ", err)
		return nil
	}
	err = l.RenewCert()
	if err != nil {
		log.WithField("节点", c.tag).Info("renew cert error: ", err)
		return nil
	}
	return nil
}

func (c *Controller) requestCert() error {
	certFile := filepath.Join("/etc/PPanel-node/", c.info.Type+strconv.Itoa(c.info.Id)+".cer")
	keyFile := filepath.Join("/etc/PPanel-node/", c.info.Type+strconv.Itoa(c.info.Id)+".key")
	switch c.info.Protocol.CertMode {
	case "none", "":
	case "file":
		if _, err := certificateStatusReport(c.info); err != nil {
			return fmt.Errorf("validate file certificate: %w", err)
		}
		if err := os.Chmod(keyFile, 0600); err != nil {
			return fmt.Errorf("secure file certificate key: %w", err)
		}
	case "dns", "http":
		if file.IsExist(certFile) && file.IsExist(keyFile) {
			if _, err := certificateStatusReport(c.info); err == nil {
				return os.Chmod(keyFile, 0600)
			}
			_ = os.Remove(certFile)
			_ = os.Remove(keyFile)
		}
		l, err := NewLego(c.info)
		if err != nil {
			return fmt.Errorf("create lego object error: %s", err)
		}
		err = l.CreateCert()
		if err != nil {
			return fmt.Errorf("create lego cert error: %s", err)
		}
	case "self":
		if file.IsExist(certFile) && file.IsExist(keyFile) {
			if _, err := certificateStatusReport(c.info); err == nil {
				return os.Chmod(keyFile, 0600)
			}
			_ = os.Remove(certFile)
			_ = os.Remove(keyFile)
		}
		err := generateSelfSslCertificate(
			c.info.Protocol.SNI,
			certFile,
			keyFile)
		if err != nil {
			return fmt.Errorf("generate self cert error: %s", err)
		}
	default:
		return fmt.Errorf("unsupported certmode: %s", c.info.Protocol.CertMode)
	}
	return nil
}

func generateSelfSslCertificate(domain, certPath, keyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		Version: 3,
		Subject: pkix.Name{
			CommonName: domain,
		},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(30, 0, 0),
	}
	if ip := net.ParseIP(domain); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{domain}
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl.SerialNumber = serial
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return err
	}
	certificate := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	})
	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err = os.WriteFile(certPath, certificate, 0644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, privateKey, 0600)
}

func certificateStatusReport(
	info *panel.NodeInfo,
) (*panel.CertificateStatusReport, error) {
	if info == nil || info.Protocol == nil {
		return nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(info.Protocol.CertMode))
	if mode == "" || mode == "none" {
		return nil, nil
	}
	certPath := filepath.Join(
		"/etc/PPanel-node/",
		info.Type+strconv.Itoa(info.Id)+".cer",
	)
	content, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read certificate metadata: %w", err)
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("decode certificate metadata")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate metadata: %w", err)
	}
	domain := strings.ToLower(strings.TrimSpace(info.Protocol.SNI))
	if domain != "" {
		if err = certificate.VerifyHostname(domain); err != nil {
			return nil, fmt.Errorf("verify certificate domain: %w", err)
		}
	}
	if !certificate.NotAfter.After(time.Now()) {
		return nil, errors.New("certificate has expired")
	}
	fingerprint := sha256.Sum256(certificate.Raw)
	return &panel.CertificateStatusReport{
		ProtocolKey: strings.ToLower(strings.TrimSpace(info.Type)) + ":" +
			strconv.Itoa(info.Protocol.Port),
		Domain:      domain,
		Fingerprint: hex.EncodeToString(fingerprint[:]),
		NotAfter:    certificate.NotAfter.UnixMilli(),
	}, nil
}
