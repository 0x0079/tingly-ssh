package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server starts a QUIC listener with a fresh self-signed key and returns its
// address, its pin, and the exporter value of every connection it accepts.
func server(t *testing.T) (string, string, <-chan []byte) {
	t.Helper()
	dir := t.TempDir()
	cert, _, err := LoadOrCreateCert(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := PinOf(cert)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen("127.0.0.1:0", ServerTLS(cert), Tuning{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	exporters := make(chan []byte, 8)
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			ekm, err := Exporter(conn)
			if err != nil {
				t.Error(err)
			}
			exporters <- ekm
		}
	}()
	return ln.Addr().String(), pin, exporters
}

func dial(t *testing.T, addr string, conf *tls.Config) (*Link, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, err := Dial(ctx, addr, conf, Tuning{})
	if err == nil {
		t.Cleanup(func() { link.Close() })
	}
	return link, err
}

// TestExporterIsSharedAndPerConnection is the property key authentication
// rests on: both ends derive the same value, and no two connections do.
func TestExporterIsSharedAndPerConnection(t *testing.T) {
	addr, pin, exporters := server(t)
	conf, err := ClientTLS("127.0.0.1", pin, false)
	if err != nil {
		t.Fatal(err)
	}
	var seen [][]byte
	for range 2 {
		link, err := dial(t, addr, conf)
		if err != nil {
			t.Fatal(err)
		}
		mine, err := link.Exporter()
		if err != nil {
			t.Fatal(err)
		}
		// The server only accepts once the client's handshake completes.
		var theirs []byte
		select {
		case theirs = <-exporters:
		case <-time.After(10 * time.Second):
			t.Fatal("server never accepted")
		}
		if len(mine) != 32 || !bytes.Equal(mine, theirs) {
			t.Fatalf("exporters differ: client %x server %x", mine, theirs)
		}
		if link.PeerPin() != pin {
			t.Fatalf("PeerPin = %s, want %s", link.PeerPin(), pin)
		}
		seen = append(seen, mine)
	}
	if bytes.Equal(seen[0], seen[1]) {
		t.Fatal("two connections share an exporter value")
	}
}

func TestTrustOnFirstUse(t *testing.T) {
	addr, pin, _ := server(t)
	path := filepath.Join(t.TempDir(), "sub", "known_servers")
	known := NewKnownServers(path)

	var learned []string
	conf, err := ClientTLSTrustOnFirstUse("127.0.0.1", addr, known, func(p string) { learned = append(learned, p) })
	if err != nil {
		t.Fatal(err)
	}
	// First contact records the key.
	if _, err := dial(t, addr, conf); err != nil {
		t.Fatal(err)
	}
	if len(learned) != 1 || learned[0] != pin {
		t.Fatalf("learned = %v, want [%s]", learned, pin)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("known_servers mode %04o, want 0600", info.Mode().Perm())
	}
	// The second connection matches silently.
	if _, err := dial(t, addr, conf); err != nil {
		t.Fatal(err)
	}
	if len(learned) != 1 {
		t.Fatalf("a known key was learned again: %v", learned)
	}

	// A different server on the same address is refused, with the fix.
	other, _, _ := server(t)
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte("# pinned\n"+strings.Replace(string(raw), addr, other, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	conf2, err := ClientTLSTrustOnFirstUse("127.0.0.1", other, known, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dial(t, other, conf2)
	if err == nil {
		t.Fatal("a changed server key was accepted")
	}
	// The client relies on the sentinel surviving quic-go to stop at once.
	if !errors.Is(err, ErrServerKeyChanged) {
		t.Fatalf("error chain lost the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("unhelpful error: %v", err)
	}
	// And the file was not rewritten to the attacker's key.
	if have, _, _ := known.Lookup(other); have != pin {
		t.Fatalf("known pin became %s", have)
	}
}

func TestKnownServersAddRefusesOverwrite(t *testing.T) {
	k := NewKnownServers(filepath.Join(t.TempDir(), "known"))
	if err := k.Add("Host:1", "sha256:AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := k.Add("host:1", "sha256:AAAA"); err != nil {
		t.Fatalf("re-adding the same pin: %v", err)
	}
	if err := k.Add("host:1", "sha256:BBBB"); !errors.Is(err, ErrServerKeyChanged) {
		t.Fatalf("overwrite: err = %v", err)
	}
}
