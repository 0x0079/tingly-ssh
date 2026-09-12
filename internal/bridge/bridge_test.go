package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x0079/tingly-shell/internal/proto"
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
	cancel    context.CancelFunc
	runErr    chan error
	runTaken  bool // a test already consumed runErr, so cleanup must not wait
}

// waitRun consumes the supervisor's exit status, so a test can assert on it
// without leaving cleanup waiting for a value that will never come.
func (h *harness) waitRun(t *testing.T, d time.Duration) error {
	t.Helper()
	h.runTaken = true
	select {
	case err := <-h.runErr:
		return err
	case <-time.After(d):
		t.Fatal("Run did not return in time")
		return nil
	}
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

	h := &harness{server: srv, client: cli, localAddr: ln.Addr().String(), cancel: cancel, runErr: runErr}
	t.Cleanup(func() {
		cancel()
		ln.Close()
		srv.Close()
		if h.runTaken {
			return
		}
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("client supervisor did not stop")
		}
	})
	return h
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

// rawSession completes a handshake by hand and returns the HELLO_ACK plus the
// link, so a test can hold a session open, abandon one without a CLOSE frame
// (what a killed client looks like to the server), or read a refusal code
// directly.
func rawSession(t *testing.T, addr, pin string, token []byte, resume bool) (*proto.HelloAck, *proto.Conn) {
	t.Helper()
	tlsConf, err := transport.ClientTLS("127.0.0.1", pin, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, err := transport.Dial(ctx, addr, tlsConf, transport.Tuning{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	pc := proto.NewConn(link)
	id, err := proto.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	hello := &proto.Hello{
		Version:   proto.Version,
		SessionID: id,
		Epoch:     1,
		Window:    proto.DefaultWindow,
		Token:     token,
	}
	if resume {
		hello.Flags |= proto.FlagResume
	}
	if err := pc.WriteAndFlush(hello); err != nil {
		t.Fatalf("send HELLO: %v", err)
	}
	f, err := pc.ReadFrame()
	if err != nil {
		t.Fatalf("read HELLO_ACK: %v", err)
	}
	ack, ok := f.(*proto.HelloAck)
	if !ok {
		t.Fatalf("want HELLO_ACK, got %s", f.Type())
	}
	return ack, pc
}

// TestSessionCapRefusesAndEvicts covers risk R-3: the server bounds how many
// sessions it holds, and a zombie left behind by a killed client must not keep
// the next client out.
func TestSessionCapRefusesAndEvicts(t *testing.T) {
	dir := t.TempDir()
	cert, _, err := transport.LoadOrCreateCert(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := transport.PinOf(cert)
	if err != nil {
		t.Fatal(err)
	}
	target := echoServer(t)
	srv, err := NewServer(ServerConfig{
		Listen:      "127.0.0.1:0",
		Targets:     []string{target.Addr().String()},
		Token:       []byte("token"),
		MaxSessions: 1,
		Linger:      time.Hour, // never reaped during the test: eviction must do it
		TLS:         transport.ServerTLS(cert),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)
	addr := srv.Addr().String()

	// One live session fills the server.
	ack, live := rawSession(t, addr, pin, []byte("token"), false)
	if ack.Code != proto.CodeOK {
		t.Fatalf("first session refused: %s", proto.CodeName(ack.Code))
	}

	// A second is refused, because the first one is still linked.
	refused, conn2 := rawSession(t, addr, pin, []byte("token"), false)
	if refused.Code != proto.CodeResourceExhausted {
		t.Fatalf("want resource_exhausted, got %s", proto.CodeName(refused.Code))
	}
	_ = conn2.Close()

	// Killing the link without a CLOSE frame is what a crashed client looks
	// like: the session lingers with nobody attached.
	_ = live.Close()
	deadline := time.Now().Add(5 * time.Second)
	for srv.sessionCount() != 1 || srv.linkedCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("zombie session never detached (sessions=%d linked=%d)",
				srv.sessionCount(), srv.linkedCount())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The next client evicts the zombie instead of being turned away.
	third, conn3 := rawSession(t, addr, pin, []byte("token"), false)
	defer conn3.Close()
	if third.Code != proto.CodeOK {
		t.Fatalf("third session refused: %s", proto.CodeName(third.Code))
	}
	if n := srv.sessionCount(); n != 1 {
		t.Fatalf("session count is %d, want 1 after eviction", n)
	}
}

func TestCertificateLifetimeIsBounded(t *testing.T) {
	dir := t.TempDir()
	cert, created, err := transport.LoadOrCreateCert(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected a freshly generated certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	// R-6: long enough not to be a chore, short enough to force renewal.
	if life := leaf.NotAfter.Sub(leaf.NotBefore); life > 830*24*time.Hour {
		t.Fatalf("certificate valid for %v, want at most ~825 days", life)
	}
}

// TestRunStopsOnContextCancel: cancelling the client must tear down a healthy
// link, not wait for it to fail on its own.
func TestRunStopsOnContextCancel(t *testing.T) {
	h := newHarness(t, "token", "token")
	waitFor(t, 10*time.Second, h.client.Session().Linked, "the client to attach")
	h.cancel()
	if err := h.waitRun(t, 5*time.Second); err == nil {
		t.Fatal("expected a shutdown error")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, limit time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
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
