package transport

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KnownServers is a trust-on-first-use store of server key pins, the
// tunnel's counterpart of ~/.ssh/known_hosts: one "host:port sha256:<pin>"
// per line.
//
// It is only safe when the client does not send a secret on the first
// connection, which is why the CLI uses it for SSH key authentication and
// never for tokens (.design/ssh-key-auth.pencil.md §4).
type KnownServers struct {
	path string
	mu   sync.Mutex
}

// ErrServerKeyChanged means a known server presented a different key.
var ErrServerKeyChanged = errors.New("transport: server key changed")

// NewKnownServers uses the file at path, which need not exist yet.
func NewKnownServers(path string) *KnownServers { return &KnownServers{path: path} }

// Path returns the backing file.
func (k *KnownServers) Path() string { return k.path }

// Lookup returns the recorded pin for addr and the line it is on.
func (k *KnownServers) Lookup(addr string) (pin string, line int, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lookupLocked(normalizeAddr(addr))
}

func (k *KnownServers) lookupLocked(addr string) (string, int, error) {
	f, err := os.Open(k.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("transport: known servers: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if normalizeAddr(fields[0]) == addr {
			return fields[1], n, nil
		}
	}
	return "", 0, sc.Err()
}

// Add records addr's pin. It refuses to overwrite a different existing entry:
// replacing a key is an operator decision, made by editing the file.
func (k *KnownServers) Add(addr, pin string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	addr = normalizeAddr(addr)
	have, _, err := k.lookupLocked(addr)
	if err != nil {
		return err
	}
	if have != "" {
		if normalizePin(have) == normalizePin(pin) {
			return nil
		}
		return fmt.Errorf("%w: %s is already known with a different key", ErrServerKeyChanged, addr)
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return fmt.Errorf("transport: known servers: %w", err)
	}
	f, err := os.OpenFile(k.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("transport: known servers: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s %s\n", addr, pin); err != nil {
		f.Close()
		return fmt.Errorf("transport: known servers: %w", err)
	}
	return f.Close()
}

func normalizeAddr(addr string) string { return strings.ToLower(strings.TrimSpace(addr)) }

// ClientTLSTrustOnFirstUse builds a client TLS configuration that trusts the
// key addr presented the first time and requires that same key afterwards.
// learned is called once when a new key is recorded, so the caller can say so
// loudly the way ssh does.
func ClientTLSTrustOnFirstUse(serverName, addr string, known *KnownServers, learned func(pin string)) (*tls.Config, error) {
	cfg, err := ClientTLS(serverName, "", false)
	if err != nil {
		return nil, err
	}
	// Chain verification is replaced by the known servers check below.
	cfg.InsecureSkipVerify = true
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("transport: server sent no certificate")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("transport: parse server certificate: %w", err)
		}
		got := Pin(leaf)
		want, line, err := known.Lookup(addr)
		if err != nil {
			return err
		}
		if want == "" {
			if err := known.Add(addr, got); err != nil {
				return err
			}
			if learned != nil {
				learned(got)
			}
			return nil
		}
		if pinsEqual(want, got) {
			return nil
		}
		return fmt.Errorf("%w for %s: known %s, got %s; if the server key was rotated on purpose, delete line %d of %s",
			ErrServerKeyChanged, addr, want, got, line, known.Path())
	}
	return cfg, nil
}

// pinsEqual compares pins by their decoded digest, so formatting differences
// in a hand-edited file do not matter.
func pinsEqual(a, b string) bool {
	da, errA := base64.StdEncoding.DecodeString(normalizePin(a))
	db, errB := base64.StdEncoding.DecodeString(normalizePin(b))
	return errA == nil && errB == nil && len(da) == sha256.Size && string(da) == string(db)
}
