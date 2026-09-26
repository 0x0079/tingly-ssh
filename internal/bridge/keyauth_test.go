package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/0x0079/tingly-ssh/internal/auth"
	"github.com/0x0079/tingly-ssh/internal/proto"
	"github.com/0x0079/tingly-ssh/internal/transport"
)

func newKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keyLine(s ssh.Signer, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " " + comment
}

// countingProver records how often the keys were asked to sign, which is how
// the tests tell a signature reconnect from a ticket reconnect.
type countingProver struct {
	inner *auth.Prover
	calls atomic.Int32
}

func (c *countingProver) Prove(m []byte) ([]proto.KeyProof, error) {
	c.calls.Add(1)
	return c.inner.Prove(m)
}

type keyServerOpts struct {
	tokens []string // credential records, if the server also accepts tokens
}

// keyServer runs a server that admits the given authorized keys lines.
func keyServer(t *testing.T, lines []string, opts keyServerOpts) (*Server, string, *auth.KeyStore) {
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
	var keys *auth.KeyStore
	if len(lines) > 0 {
		keys, err = auth.NewKeyStore([]byte(strings.Join(lines, "\n")))
		if err != nil {
			t.Fatal(err)
		}
	}
	var creds *auth.Store
	if len(opts.tokens) > 0 {
		path := filepath.Join(dir, "credentials")
		if err := os.WriteFile(path, []byte(strings.Join(opts.tokens, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if creds, err = auth.LoadStore(path); err != nil {
			t.Fatal(err)
		}
	}
	target := echoServer(t)
	srv, err := NewServer(ServerConfig{
		Listen:      "127.0.0.1:0",
		Targets:     []string{target.Addr().String()},
		Credentials: creds,
		Keys:        keys,
		Linger:      time.Hour,
		TLS:         transport.ServerTLS(cert),
		Logger:      testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); srv.Close() })
	go srv.Serve(ctx)
	return srv, pin, keys
}

func testLogger() *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.DiscardHandler)
}

type keyClient struct {
	cli    *Client
	prover *countingProver
	local  string
	runErr chan error
}

// startKeyClient runs a client that authenticates with signers.
func startKeyClient(t *testing.T, srv *Server, pin string, signers ...ssh.Signer) *keyClient {
	t.Helper()
	conf, err := transport.ClientTLS("127.0.0.1", pin, false)
	if err != nil {
		t.Fatal(err)
	}
	prover := &countingProver{inner: auth.NewSignerProver(signers...)}
	cli, err := NewClient(ClientConfig{
		Server: srv.Addr().String(),
		Target: srv.cfg.Targets[0],
		Keys:   prover,
		Linger: 5 * time.Second,
		TLS:    conf,
		Logger: testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- cli.Run(ctx) }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go cli.ServeListener(ctx, ln)
	t.Cleanup(func() { cancel(); ln.Close() })
	return &keyClient{cli: cli, prover: prover, local: ln.Addr().String(), runErr: runErr}
}

// echo sends msg through the tunnel and checks it comes back.
func echo(t *testing.T, conn net.Conn, msg string) {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("echo %q: %v", msg, err)
	}
	if string(buf) != msg {
		t.Fatalf("echo: got %q want %q", buf, msg)
	}
}

func (k *keyClient) waitRefused(t *testing.T) *HandshakeError {
	t.Helper()
	select {
	case err := <-k.runErr:
		var he *HandshakeError
		if !errors.As(err, &he) {
			t.Fatalf("Run ended with %v, want a handshake refusal", err)
		}
		return he
	case <-time.After(10 * time.Second):
		t.Fatal("client kept retrying a refused key")
		return nil
	}
}

// TestKeyAuthEndToEnd is the whole feature: no token, no new secret, and a
// session that survives link drops without asking the key to sign again.
func TestKeyAuthEndToEnd(t *testing.T) {
	alice := newKey(t)
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice@laptop")}, keyServerOpts{})
	kc := startKeyClient(t, srv, pin, newKey(t), alice) // an unauthorized agent key rides along

	conn, err := net.Dial("tcp", kc.local)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo(t, conn, "before")

	for i := range 2 {
		waitFor(t, 5*time.Second, func() bool { return srv.linkedCount() == 1 }, "link attached")
		if !kc.cli.Session().DropLink() {
			t.Fatalf("drop %d: no link to drop", i)
		}
		echo(t, conn, "after drop")
	}
	// One signature for the first link; both reconnects used the ticket.
	if n := kc.prover.calls.Load(); n != 1 {
		t.Fatalf("keys signed %d times, want 1 (reconnects must use the ticket)", n)
	}
	// The session is bound to the key, not to a shared label.
	srv.mu.Lock()
	for _, sess := range srv.sessions {
		if want := auth.KeyIDPrefix + ssh.FingerprintSHA256(alice.PublicKey()); sess.Identity() != want {
			t.Errorf("session identity %q, want %q", sess.Identity(), want)
		}
	}
	srv.mu.Unlock()
}

func TestUnauthorizedKeyStopsTheClient(t *testing.T) {
	srv, pin, _ := keyServer(t, []string{keyLine(newKey(t), "someone-else")}, keyServerOpts{})
	kc := startKeyClient(t, srv, pin, newKey(t))
	he := kc.waitRefused(t)
	if he.Code != proto.CodeUnauthorized || !strings.Contains(he.Reason, "no offered SSH key is authorized") {
		t.Fatalf("refusal = %s (%s)", proto.CodeName(he.Code), he.Reason)
	}
}

// rawKeyHello completes a handshake by hand, letting build fill in the key
// fields once the connection's exporter is known.
func rawKeyHello(t *testing.T, addr, pin string, id proto.SessionID, epoch uint64, flags uint64,
	build func(exporter []byte, h *proto.Hello)) (*proto.HelloAck, *proto.Conn) {
	t.Helper()
	conf, err := transport.ClientTLS("127.0.0.1", pin, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	link, err := transport.Dial(ctx, addr, conf, transport.Tuning{})
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := link.Exporter()
	if err != nil {
		t.Fatal(err)
	}
	h := &proto.Hello{Version: proto.Version, SessionID: id, Epoch: epoch, Flags: flags | proto.FlagKeyAuth, Window: proto.DefaultWindow}
	build(exporter, h)
	pc := proto.NewConn(link)
	t.Cleanup(func() { pc.Close() })
	ack, err := clientHandshake(pc, h)
	var he *HandshakeError
	if errors.As(err, &he) {
		return &proto.HelloAck{Code: he.Code, Reason: he.Reason}, pc
	}
	if err != nil {
		t.Fatal(err)
	}
	return ack, pc
}

func proofsFor(t *testing.T, s ssh.Signer, message []byte) []proto.KeyProof {
	t.Helper()
	p, err := auth.NewSignerProver(s).Prove(message)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRelayedProofIsRefused is the man-in-the-middle case: a proof made on
// the attacker's connection to the victim does not open the real server.
func TestRelayedProofIsRefused(t *testing.T) {
	alice := newKey(t)
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice")}, keyServerOpts{})
	id, _ := proto.NewSessionID()
	ack, _ := rawKeyHello(t, srv.Addr().String(), pin, id, 1, 0, func(_ []byte, h *proto.Hello) {
		stolen := auth.HelloMessage([]byte("the exporter of some other TLS connection"), id)
		h.Proofs = proofsFor(t, alice, stolen)
	})
	if ack.Code != proto.CodeUnauthorized || !strings.Contains(ack.Reason, "does not match this connection") {
		t.Fatalf("relayed proof: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
}

func TestTicketRules(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice"), keyLine(bob, "bob")}, keyServerOpts{})
	addr := srv.Addr().String()

	open := func(s ssh.Signer) (proto.SessionID, []byte) {
		id, _ := proto.NewSessionID()
		ack, _ := rawKeyHello(t, addr, pin, id, 1, 0, func(e []byte, h *proto.Hello) {
			h.Proofs = proofsFor(t, s, auth.HelloMessage(e, id))
		})
		if ack.Code != proto.CodeOK || len(ack.Ticket) != 32 {
			t.Fatalf("open: %s (%s), ticket %d bytes", proto.CodeName(ack.Code), ack.Reason, len(ack.Ticket))
		}
		return id, ack.Ticket
	}
	aliceID, aliceTicket := open(alice)
	bobID, _ := open(bob)

	withTicket := func(id proto.SessionID, epoch, flags uint64, tk []byte) *proto.HelloAck {
		ack, _ := rawKeyHello(t, addr, pin, id, epoch, flags, func(_ []byte, h *proto.Hello) { h.Ticket = tk })
		return ack
	}

	// The owner resumes with its ticket; no new ticket is minted for that.
	if ack := withTicket(aliceID, 2, proto.FlagResume, aliceTicket); ack.Code != proto.CodeOK || len(ack.Ticket) != 0 {
		t.Fatalf("ticket resume: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	// It cannot open anybody else's session, nor a new one.
	if ack := withTicket(bobID, 9, proto.FlagResume, aliceTicket); ack.Code != proto.CodeUnauthorized {
		t.Errorf("alice's ticket on bob's session: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	fresh, _ := proto.NewSessionID()
	if ack := withTicket(fresh, 1, proto.FlagResume, aliceTicket); ack.Code != proto.CodeSessionUnknown {
		t.Errorf("ticket for an unknown session: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	if ack := withTicket(aliceID, 10, 0, aliceTicket); ack.Code != proto.CodeProtocol {
		t.Errorf("ticket without RESUME: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	forged := make([]byte, 32)
	if ack := withTicket(aliceID, 11, proto.FlagResume, forged); ack.Code != proto.CodeUnauthorized {
		t.Errorf("forged ticket: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}

	// Signing again replaces the ticket: the old one stops working.
	ack, _ := rawKeyHello(t, addr, pin, aliceID, 12, proto.FlagResume, func(e []byte, h *proto.Hello) {
		h.Proofs = proofsFor(t, alice, auth.HelloMessage(e, aliceID))
	})
	if ack.Code != proto.CodeOK || len(ack.Ticket) != 32 {
		t.Fatalf("re-sign: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	if old := withTicket(aliceID, 13, proto.FlagResume, aliceTicket); old.Code != proto.CodeUnauthorized {
		t.Errorf("superseded ticket: %s (%s)", proto.CodeName(old.Code), old.Reason)
	}
	if cur := withTicket(aliceID, 14, proto.FlagResume, ack.Ticket); cur.Code != proto.CodeOK {
		t.Errorf("current ticket: %s (%s)", proto.CodeName(cur.Code), cur.Reason)
	}
}

func TestProofsAndTicketAreExclusive(t *testing.T) {
	alice := newKey(t)
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice")}, keyServerOpts{})
	id, _ := proto.NewSessionID()
	ack, _ := rawKeyHello(t, srv.Addr().String(), pin, id, 1, 0, func(e []byte, h *proto.Hello) {
		h.Proofs = proofsFor(t, alice, auth.HelloMessage(e, id))
		h.Token = []byte("and a token too")
	})
	if ack.Code != proto.CodeProtocol {
		t.Fatalf("token plus proofs: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
}

// TestRevokedKeyCannotResume keeps ADR-0004's revocation promise with tickets:
// deleting a key's line stops its sessions from reconnecting.
func TestRevokedKeyCannotResume(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	srv, pin, keys := keyServer(t, []string{keyLine(alice, "alice"), keyLine(bob, "bob")}, keyServerOpts{})
	kc := startKeyClient(t, srv, pin, alice)
	conn, err := net.Dial("tcp", kc.local)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo(t, conn, "hello")

	if err := keys.Replace([]byte(keyLine(bob, "bob"))); err != nil {
		t.Fatal(err)
	}
	kc.cli.Session().DropLink()
	he := kc.waitRefused(t)
	if he.Code != proto.CodeUnauthorized || !strings.Contains(he.Reason, "no longer authorized") {
		t.Fatalf("revoked resume: %s (%s)", proto.CodeName(he.Code), he.Reason)
	}
}

// TestTicketOnlyGoesToTheIssuingServer: if the server key changes mid-session
// the client signs afresh rather than handing its ticket to a stranger.
func TestTicketOnlyGoesToTheIssuingServer(t *testing.T) {
	alice := newKey(t)
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice")}, keyServerOpts{})
	kc := startKeyClient(t, srv, pin, alice)
	conn, err := net.Dial("tcp", kc.local)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo(t, conn, "one")
	waitFor(t, 5*time.Second, func() bool { return srv.linkedCount() == 1 }, "link attached")

	// Pretend the ticket came from another server key. Run owns these fields,
	// but it is parked in Attach while the link is up.
	kc.cli.ticketPin = "sha256:" + strings.Repeat("A", 43) + "="
	kc.cli.Session().DropLink()
	echo(t, conn, "two")
	if n := kc.prover.calls.Load(); n != 2 {
		t.Fatalf("keys signed %d times, want 2: the ticket must not go to an unknown server key", n)
	}
}

func TestTokensAndKeysCoexist(t *testing.T) {
	alice := newKey(t)
	tok := []byte("ci-runner-token")
	srv, pin, _ := keyServer(t, []string{keyLine(alice, "alice")}, keyServerOpts{
		tokens: []string{auth.Record("ci-runner", tok, time.Time{})},
	})
	ack, c := rawSession(t, srv.Addr().String(), pin, tok, false)
	c.Close()
	if ack.Code != proto.CodeOK {
		t.Fatalf("token client: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
	kc := startKeyClient(t, srv, pin, alice)
	conn, err := net.Dial("tcp", kc.local)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo(t, conn, "both work")
}

func TestServerRefusesMethodItDoesNotAccept(t *testing.T) {
	// Tokens only: a key client gets a reason, not a mystery.
	tok := []byte("t")
	srv, pin, _ := keyServer(t, nil, keyServerOpts{tokens: []string{auth.Record("x", tok, time.Time{})}})
	kc := startKeyClient(t, srv, pin, newKey(t))
	if he := kc.waitRefused(t); !strings.Contains(he.Reason, "does not accept SSH keys") {
		t.Fatalf("key client on a token server: %s", he.Reason)
	}

	// Keys only: a token client is told the same.
	srv2, pin2, _ := keyServer(t, []string{keyLine(newKey(t), "k")}, keyServerOpts{})
	ack, c := rawSession(t, srv2.Addr().String(), pin2, tok, false)
	c.Close()
	if ack.Code != proto.CodeUnauthorized || !strings.Contains(ack.Reason, "only accepts SSH keys") {
		t.Fatalf("token client on a key server: %s (%s)", proto.CodeName(ack.Code), ack.Reason)
	}
}

func TestClientNeedsExactlyOneMethod(t *testing.T) {
	if _, err := NewClient(ClientConfig{Server: "x:1"}); err == nil {
		t.Error("client with no credential accepted")
	}
	if _, err := NewClient(ClientConfig{Server: "x:1", Token: []byte("t"), Keys: auth.NewSignerProver(newKey(t))}); err == nil {
		t.Error("client with both a token and keys accepted")
	}
}
