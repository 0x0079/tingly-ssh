package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x0079/tingly-shell/internal/transport"
)

// echoServer stands in for sshd: it echoes every byte back.
func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

type harness struct {
	server    *Server
	client    *Client
	localAddr string
}

func newHarness(t *testing.T, token, clientToken string) *harness {
	t.Helper()
	dir := t.TempDir()
	cert, _, err := transport.LoadOrCreateCert(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := transport.PinOf(cert)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	target := echoServer(t)
	srv, err := NewServer(ServerConfig{
		Listen:  "127.0.0.1:0",
		Targets: []string{target.Addr().String()},
		Token:   []byte(token),
		Linger:  5 * time.Second,
		TLS:     transport.ServerTLS(cert),
		Logger:  logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)

	clientTLS, err := transport.ClientTLS("127.0.0.1", pin, false)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := NewClient(ClientConfig{
		Server: srv.Addr().String(),
		Target: target.Addr().String(),
		Token:  []byte(clientToken),
		Linger: 5 * time.Second,
		TLS:    clientTLS,
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- cli.Run(ctx) }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go cli.ServeListener(ctx, ln)

	t.Cleanup(func() {
		cancel()
		ln.Close()
		srv.Close()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("client supervisor did not stop")
		}
	})
	return &harness{server: srv, client: cli, localAddr: ln.Addr().String()}
}

func TestEndToEndEcho(t *testing.T) {
	h := newHarness(t, "token", "token")
	conn, err := net.Dial("tcp", h.localAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("SSH-2.0-OpenSSH_9.6\r\n"))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "SSH-2.0-OpenSSH_9.6\r\n" {
		t.Fatalf("got %q", buf)
	}
}

// TestEndToEndSurvivesLinkDrop is the product promise: the bridged TCP
// connection stays up while the QUIC link underneath it dies and is rebuilt.
func TestEndToEndSurvivesLinkDrop(t *testing.T) {
	h := newHarness(t, "token", "token")
	conn, err := net.Dial("tcp", h.localAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	const chunk = 64 << 10
	const rounds = 8
	payload := make([]byte, chunk)
	rand.Read(payload)

	for i := range rounds {
		if i%2 == 1 && !h.client.Session().DropLink() {
			t.Fatal("no link to drop")
		}
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("round %d: write: %v", i, err)
		}
		got := make([]byte, chunk)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("round %d: read: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("round %d: echoed data differs", i)
		}
	}
}

func TestBadTokenIsRejected(t *testing.T) {
	h := newHarness(t, "right", "wrong")
	start := time.Now()
	select {
	case <-h.client.Session().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("client did not give up on an invalid token")
	}
	err := h.client.Session().Err()
	if err == nil {
		t.Fatal("expected an authentication failure")
	}
	// The refusal must survive the connection teardown, otherwise the client
	// cannot tell a rejected token from a broken network and keeps retrying.
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("want an unauthorized reason, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("gave up after %s; a refused token must fail on the first attempt", elapsed)
	}
}

func TestPinMismatchIsRejected(t *testing.T) {
	dir := t.TempDir()
	cert, _, err := transport.LoadOrCreateCert(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	_, _, err = transport.LoadOrCreateCert(filepath.Join(other, "c.pem"), filepath.Join(other, "k.pem"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	wrongCert, _, err := transport.LoadOrCreateCert(filepath.Join(other, "c.pem"), filepath.Join(other, "k.pem"), nil)
	if err != nil {
		t.Fatal(err)
	}
	wrongPin, err := transport.PinOf(wrongCert)
	if err != nil {
		t.Fatal(err)
	}

	target := echoServer(t)
	srv, err := NewServer(ServerConfig{
		Listen:  "127.0.0.1:0",
		Targets: []string{target.Addr().String()},
		Token:   []byte("t"),
		TLS:     transport.ServerTLS(cert),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	clientTLS, err := transport.ClientTLS("127.0.0.1", wrongPin, false)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := NewClient(ClientConfig{
		Server: srv.Addr().String(),
		Target: target.Addr().String(),
		Token:  []byte("t"),
		Linger: 1500 * time.Millisecond,
		TLS:    clientTLS,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = cli.Run(ctx)
	if err == nil {
		t.Fatal("expected the pinned handshake to fail")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.LoadToken(path); err == nil {
		t.Fatal("a world-readable token file must be rejected")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := transport.LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(tok) != "secret" {
		t.Fatalf("got %q", tok)
	}
}
