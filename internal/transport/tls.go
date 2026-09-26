// Package transport wires the session layer onto QUIC: TLS material, pinning
// and dial/listen helpers. See docs/04-security-model.md.
package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x0079/tingly-ssh/internal/proto"
)

// PinPrefix marks a SPKI pin string.
const PinPrefix = "sha256:"

// CertLifetime is how long a generated self-signed certificate is valid.
//
// 825 days is the longest lifetime the CA/Browser Forum still accepts, and it
// keeps the habit of renewing rather than minting a certificate that outlives
// the machine. Renewal is cheap here: clients pin the public key, so reusing
// the key means the pin does not change and no client configuration moves.
const CertLifetime = 825 * 24 * time.Hour

// Pin returns the pin for a parsed certificate: base64(SHA-256(SPKI)).
// Pinning the public key rather than the certificate means the server can
// renew its certificate without invalidating client configuration.
func Pin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return PinPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// PinOf returns the pin of a loaded key pair's leaf certificate.
func PinOf(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("transport: certificate has no leaf")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("transport: parse leaf: %w", err)
	}
	return Pin(leaf), nil
}

func normalizePin(pin string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pin), PinPrefix))
}

// ServerTLS builds the server side TLS configuration.
func ServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{proto.ALPN},
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLS builds the client side TLS configuration.
//
// With a pin, the certificate chain is validated against that pin alone, which
// is what makes self-signed server certificates safe to use. Without a pin the
// system trust store applies. insecure skips verification entirely and is for
// local development only.
func ClientTLS(serverName, pin string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{
		ServerName: serverName,
		NextProtos: []string{proto.ALPN},
		MinVersion: tls.VersionTLS13,
	}
	switch {
	case pin != "":
		want, err := base64.StdEncoding.DecodeString(normalizePin(pin))
		if err != nil {
			return nil, fmt.Errorf("transport: decode pin: %w", err)
		}
		if len(want) != sha256.Size {
			return nil, fmt.Errorf("transport: pin must be %d bytes, got %d", sha256.Size, len(want))
		}
		// Chain verification is replaced, not skipped: VerifyPeerCertificate
		// below is the authentication decision.
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("transport: server sent no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("transport: parse server certificate: %w", err)
			}
			if got := Pin(leaf); normalizePin(got) != normalizePin(pin) {
				return fmt.Errorf("transport: server key pin mismatch: got %s", got)
			}
			return nil
		}
	case insecure:
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

// LoadOrCreateCert returns the key pair at certPath/keyPath, generating a
// self-signed Ed25519 certificate if both are absent.
func LoadOrCreateCert(certPath, keyPath string, hosts []string) (tls.Certificate, bool, error) {
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if certErr == nil && keyErr == nil {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		return cert, false, err
	}
	if certErr == nil || keyErr == nil {
		return tls.Certificate{}, false, fmt.Errorf("transport: found only one of %s and %s", certPath, keyPath)
	}
	cert, err := generateSelfSigned(certPath, keyPath, hosts)
	return cert, true, err
}

func generateSelfSigned(certPath, keyPath string, hosts []string) (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "tingly-ssh"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(CertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	if len(tmpl.DNSNames) == 0 && len(tmpl.IPAddresses) == 0 {
		tmpl.DNSNames = []string{"tingly-ssh"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("transport: write %s: %w", path, err)
	}
	return nil
}

// LoadToken reads a pre-shared token and refuses world- or group-readable
// files, which is the most common way a shared secret leaks.
func LoadToken(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("transport: token file: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("transport: token file %s has mode %04o, want 0600", path, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("transport: token file: %w", err)
	}
	tok := []byte(strings.TrimSpace(string(raw)))
	if len(tok) == 0 {
		return nil, fmt.Errorf("transport: token file %s is empty", path)
	}
	return tok, nil
}

// NewToken returns a fresh base64 token for `tingly-ssh keygen`.
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
