package mux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// pair is a client/server session pair that can be linked, cut and relinked,
// which is how docs/06-testing.md §2 injects link failures without a network.
type pair struct {
	client *Session
	server *Session
}

func newPair(t *testing.T, window uint64) *pair {
	t.Helper()
	id, err := proto.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return &pair{
		client: New(Config{Role: RoleClient, SessionID: id, Window: window, KeepAlive: time.Hour}),
		server: New(Config{Role: RoleServer, SessionID: id, Window: window, KeepAlive: time.Hour}),
	}
}

// connect attaches a fresh link and returns a function that kills it and waits
// for both sides to detach.
func (p *pair) connect(t *testing.T) func() {
	t.Helper()
	a, b := net.Pipe()
	clientStates, serverStates := p.client.States(), p.server.States()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.client.Attach(proto.NewConn(a), p.server.Window(), serverStates)
	}()
	go func() {
		defer wg.Done()
		p.server.Attach(proto.NewConn(b), p.client.Window(), clientStates)
	}()
	return func() {
		a.Close()
		b.Close()
		wg.Wait()
	}
}

func mustAccept(t *testing.T, s *Session) *Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := s.Accept(ctx)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return st
}

func TestStreamRoundTrip(t *testing.T) {
	p := newPair(t, 64<<10)
	cut := p.connect(t)
	defer cut()

	cs, err := p.client.Open(context.Background(), "127.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Write([]byte("SSH-2.0-tingly")); err != nil {
		t.Fatal(err)
	}
	ss := mustAccept(t, p.server)
	if ss.Target() != "127.0.0.1:22" {
		t.Fatalf("target = %q", ss.Target())
	}
	buf := make([]byte, 14)
	if _, err := io.ReadFull(ss, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "SSH-2.0-tingly" {
		t.Fatalf("got %q", buf)
	}
	if _, err := ss.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(cs, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "pong" {
		t.Fatalf("got %q", got)
	}
}

func TestHalfClose(t *testing.T) {
	p := newPair(t, 64<<10)
	defer p.connect(t)()

	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	cs.Write([]byte("request"))
	if err := cs.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	ss := mustAccept(t, p.server)
	body, err := io.ReadAll(ss)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if string(body) != "request" {
		t.Fatalf("got %q", body)
	}
	// The reverse direction must still work after the half close.
	if _, err := ss.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if err := ss.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	resp, err := io.ReadAll(cs)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp) != "response" {
		t.Fatalf("got %q", resp)
	}
	if _, err := cs.Write([]byte("late")); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("write after CloseWrite: %v", err)
	}
}

// TestResumeAcrossLinkFailures is the central correctness test: invariants
// I1-I3 of docs/03-session-resumption.md. The link is cut repeatedly while a
// large transfer is in flight, and the received bytes must match exactly.
func TestResumeAcrossLinkFailures(t *testing.T) {
	const total = 3 << 20
	p := newPair(t, 128<<10)

	payload := make([]byte, total)
	rng := rand.New(rand.NewSource(1))
	rng.Read(payload)

	cut := p.connect(t)
	cs, err := p.client.Open(context.Background(), "bulk")
	if err != nil {
		t.Fatal(err)
	}

	writeDone := make(chan error, 1)
	go func() {
		n, err := cs.Write(payload)
		if err == nil && n != total {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = cs.CloseWrite()
		}
		writeDone <- err
	}()

	ss := mustAccept(t, p.server)
	readDone := make(chan []byte, 1)
	go func() {
		got, err := io.ReadAll(ss)
		if err != nil {
			t.Errorf("server read: %v", err)
		}
		readDone <- got
	}()

	// Force at least one failure immediately, then keep cutting.
	cut()
	cut = p.connect(t)

	deadline := time.After(30 * time.Second)
	var got []byte
	cuts := 1
loop:
	for {
		select {
		case got = <-readDone:
			break loop
		case err := <-writeDone:
			if err != nil {
				t.Fatalf("client write: %v", err)
			}
		case <-time.After(2 * time.Millisecond):
			cut()
			cuts++
			cut = p.connect(t)
		case <-deadline:
			t.Fatal("transfer did not finish")
		}
	}
	cut()
	if cuts == 0 {
		t.Fatal("test did not exercise any link failure")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch after %d link failures: got %d bytes, want %d", cuts, len(got), total)
	}
	t.Logf("transferred %d bytes across %d link failures", total, cuts)
}

// TestWriteBlocksWhileUnlinked covers invariant I5: with no link attached the
// application blocks instead of seeing an error.
func TestWriteBlocksWhileUnlinked(t *testing.T) {
	p := newPair(t, 64<<10)
	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := cs.Write([]byte("buffered before any link exists"))
		done <- err
	}()

	select {
	case err := <-done:
		// Writing into an empty send buffer must not fail; it may return
		// immediately only once a link exists.
		t.Fatalf("write returned before a link was attached: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	defer p.connect(t)()
	if err := <-done; err != nil {
		t.Fatalf("write after attach: %v", err)
	}
	ss := mustAccept(t, p.server)
	buf := make([]byte, len("buffered before any link exists"))
	if _, err := io.ReadFull(ss, buf); err != nil {
		t.Fatal(err)
	}
}

func TestReadBlocksWhileUnlinkedThenResumes(t *testing.T) {
	p := newPair(t, 64<<10)
	cut := p.connect(t)
	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	cs.Write([]byte("first"))
	ss := mustAccept(t, p.server)
	buf := make([]byte, 5)
	if _, err := io.ReadFull(ss, buf); err != nil {
		t.Fatal(err)
	}
	cut()

	read := make(chan string, 1)
	go func() {
		b := make([]byte, 6)
		if _, err := io.ReadFull(ss, b); err != nil {
			t.Errorf("read after reconnect: %v", err)
			read <- ""
			return
		}
		read <- string(b)
	}()

	// Written while the session has no link at all.
	if _, err := cs.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-read:
		t.Fatalf("read completed with no link: %q", s)
	case <-time.After(50 * time.Millisecond):
	}

	defer p.connect(t)()
	select {
	case s := <-read:
		if s != "second" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read did not resume")
	}
}

func TestResetPropagates(t *testing.T) {
	p := newPair(t, 64<<10)
	defer p.connect(t)()
	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	cs.Write([]byte("x"))
	ss := mustAccept(t, p.server)
	io.ReadFull(ss, make([]byte, 1))

	ss.Close() // not gracefully finished -> RESET

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := cs.Read(make([]byte, 1)); err != nil {
			var rst *StreamResetError
			if !errors.As(err, &rst) {
				t.Fatalf("want reset error, got %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("reset never reached the peer")
		}
	}
}

func TestPeerCloseTerminatesSession(t *testing.T) {
	p := newPair(t, 64<<10)
	defer p.connect(t)()
	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	cs.Write([]byte("x"))
	mustAccept(t, p.server)

	p.server.Close(proto.CodeShutdown, "going away")

	select {
	case <-p.client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client session did not terminate")
	}
	var pce *PeerCloseError
	if err := p.client.Err(); !errors.As(err, &pce) {
		t.Fatalf("want PeerCloseError, got %v", err)
	}
	if _, err := cs.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on a terminated session must fail")
	}
}

func TestAdoptEpochRejectsStaleLinks(t *testing.T) {
	s := New(Config{Role: RoleServer})
	if !s.AdoptEpoch(1) {
		t.Fatal("first epoch must be adopted")
	}
	if s.AdoptEpoch(1) {
		t.Fatal("replayed epoch must be rejected")
	}
	if s.AdoptEpoch(0) {
		t.Fatal("older epoch must be rejected")
	}
	if !s.AdoptEpoch(2) {
		t.Fatal("newer epoch must be adopted")
	}
}

func TestFlowControlBoundsMemory(t *testing.T) {
	const window = 32 << 10
	p := newPair(t, window)
	defer p.connect(t)()
	cs, err := p.client.Open(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	// The server never reads, so the sender must stall at the window.
	mustAcceptAfterWrite := make(chan struct{})
	go func() {
		cs.Write(make([]byte, 1<<20))
		close(mustAcceptAfterWrite)
	}()
	mustAccept(t, p.server)

	select {
	case <-mustAcceptAfterWrite:
		t.Fatal("write of 1 MiB completed with a 32 KiB window and no reader")
	case <-time.After(200 * time.Millisecond):
	}

	p.client.mu.Lock()
	buffered := p.client.streams[1].send.buffered()
	p.client.mu.Unlock()
	if buffered > 2*window {
		t.Fatalf("replay buffer grew to %d bytes with a %d byte window", buffered, window)
	}
}
