// Package mux implements the resumable session layer described in
// docs/03-session-resumption.md: logical stream multiplexing with absolute
// byte offsets, so a session survives the death of the link underneath it.
package mux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// Role says which end of the session this is. Only clients open streams.
type Role int

const (
	RoleClient Role = iota
	RoleServer
)

func (r Role) String() string {
	if r == RoleServer {
		return "server"
	}
	return "client"
}

// ErrSessionClosed is the base error for a terminated session.
var ErrSessionClosed = errors.New("mux: session closed")

// PeerCloseError reports that the peer terminated the session with CLOSE.
type PeerCloseError struct {
	Code   uint64
	Reason string
}

func (e *PeerCloseError) Error() string {
	return fmt.Sprintf("mux: peer closed session (%s): %s", proto.CodeName(e.Code), e.Reason)
}

func (e *PeerCloseError) Is(target error) bool { return target == ErrSessionClosed }

// ProtocolError reports a peer that violated the wire contract. It is always
// fatal for the session: we never silently drop or duplicate bytes.
type ProtocolError struct{ err error }

func (e *ProtocolError) Error() string { return "mux: protocol error: " + e.err.Error() }
func (e *ProtocolError) Unwrap() error { return e.err }

// Config parameterises a Session.
type Config struct {
	Role      Role
	SessionID proto.SessionID
	// Identity names the credential this session belongs to. The server binds
	// sessions to it so one credential cannot take over another's session
	// (risk R-2 in docs/04-security-model.md §8).
	Identity   string
	Window     uint64 // our per-stream receive window
	MaxStreams int
	KeepAlive  time.Duration
	Logger     *slog.Logger
}

func (c *Config) withDefaults() {
	if c.Window == 0 {
		c.Window = proto.DefaultWindow
	}
	if c.MaxStreams == 0 {
		c.MaxStreams = 64
	}
	if c.KeepAlive == 0 {
		c.KeepAlive = 15 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
}

// Session is a resumable multiplexed session. It outlives the links that
// carry it: a link failure never fails a Session or its Streams.
type Session struct {
	id       proto.SessionID
	identity string
	role     Role
	window   uint64
	cfg      Config
	log      *slog.Logger

	mu      sync.Mutex
	cond    *sync.Cond
	streams map[uint64]*Stream
	order   []uint64 // round-robin service order
	rr      int
	nextID  uint64
	ctrl    []proto.Frame

	peerWindow uint64
	epoch      uint64
	link       *proto.Conn
	linkGen    uint64
	detachedAt time.Time

	wake     chan struct{}
	incoming chan *Stream

	closed   bool
	closeErr error
	done     chan struct{}
}

// New creates a session. The caller attaches links to it as they come and go.
func New(cfg Config) *Session {
	cfg.withDefaults()
	s := &Session{
		id:         cfg.SessionID,
		identity:   cfg.Identity,
		role:       cfg.Role,
		window:     cfg.Window,
		cfg:        cfg,
		log:        cfg.Logger.With("session", cfg.SessionID.Short(), "role", cfg.Role.String()),
		streams:    make(map[uint64]*Stream),
		nextID:     1,
		peerWindow: proto.DefaultWindow,
		wake:       make(chan struct{}, 1),
		incoming:   make(chan *Stream, cfg.MaxStreams),
		done:       make(chan struct{}),
		detachedAt: time.Now(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// ID returns the session identifier.
func (s *Session) ID() proto.SessionID { return s.id }

// Identity returns the credential label this session belongs to, or "" when
// the session was created without one.
func (s *Session) Identity() string { return s.identity }

// Window is our advertised per-stream receive window.
func (s *Session) Window() uint64 { return s.window }

// Done is closed when the session terminates.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns the termination error, or nil while the session is alive.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

// AdoptEpoch accepts a link generation strictly newer than any seen before.
// It implements the EPOCH_STALE rule in docs/02-wire-protocol.md §2.
func (s *Session) AdoptEpoch(epoch uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch <= s.epoch {
		return false
	}
	s.epoch = epoch
	return true
}

// Linked reports whether a link is currently attached.
func (s *Session) Linked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.link != nil
}

// DetachedSince reports when the last link went away. It is meaningless while
// Linked reports true.
func (s *Session) DetachedSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detachedAt
}

// States snapshots every live stream for a HELLO or HELLO_ACK.
func (s *Session) States() []proto.StreamState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.StreamState, 0, len(s.streams))
	for _, id := range s.order {
		if st, ok := s.streams[id]; ok {
			out = append(out, st.stateLocked())
		}
	}
	return out
}

func (s *Session) notifyLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Session) enqueueLocked(f proto.Frame) {
	s.ctrl = append(s.ctrl, f)
	s.notifyLocked()
}

func (s *Session) addStreamLocked(st *Stream) {
	s.streams[st.id] = st
	s.order = append(s.order, st.id)
}

func (s *Session) removeStreamLocked(id uint64) {
	if _, ok := s.streams[id]; !ok {
		return
	}
	delete(s.streams, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.rr > len(s.order) {
		s.rr = 0
	}
	s.cond.Broadcast()
}

// Open creates a new logical stream. Only a client may call it.
func (s *Session) Open(ctx context.Context, target string) (*Stream, error) {
	if s.role != RoleClient {
		return nil, errors.New("mux: only the client opens streams")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, s.closeErr
	}
	if len(s.streams) >= s.cfg.MaxStreams {
		return nil, fmt.Errorf("mux: stream limit reached (%d)", s.cfg.MaxStreams)
	}
	st := &Stream{sess: s, id: s.nextID, target: target, needsOpen: true}
	s.nextID += 2
	if s.link != nil {
		st.sendLimit = s.peerWindow
	}
	s.addStreamLocked(st)
	s.notifyLocked()
	return st, nil
}

// Accept returns the next stream opened by the peer.
func (s *Session) Accept(ctx context.Context) (*Stream, error) {
	select {
	case st := <-s.incoming:
		return st, nil
	case <-s.done:
		return nil, s.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close terminates the session, telling the peer not to resume it. The CLOSE
// frame is handed to the writer rather than written here, so the link is only
// ever touched by one goroutine.
func (s *Session) Close(code uint64, reason string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	linked := s.link != nil
	if linked {
		s.enqueueLocked(&proto.Close{Code: code, Reason: reason})
	}
	s.mu.Unlock()
	if linked {
		// Give the writer a moment to put CLOSE on the wire; it terminates the
		// session itself once flushed.
		select {
		case <-s.done:
			return nil
		case <-time.After(closeFlushTimeout):
		}
	}
	s.terminate(fmt.Errorf("%w: local close (%s): %s", ErrSessionClosed, proto.CodeName(code), reason))
	return nil
}

// closeFlushTimeout bounds how long Close waits for the CLOSE frame to reach
// the wire before terminating regardless.
const closeFlushTimeout = 250 * time.Millisecond

// terminate fails the session and every stream on it. Unlike a link failure,
// this is visible to the application (invariant I5 only covers link loss).
func (s *Session) terminate(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeErr = err
	for _, st := range s.streams {
		st.failLocked(err)
	}
	link := s.link
	s.link = nil
	s.cond.Broadcast()
	s.notifyLocked()
	close(s.done)
	s.mu.Unlock()
	if link != nil {
		// A CLOSE frame may still be queued; let it land so the peer learns
		// the session is gone instead of trying to resume it.
		_ = link.CloseGracefully(closeFlushTimeout)
	}
	s.log.Debug("session terminated", "err", err)
}

// link state for one attached link generation.
type linkState struct {
	conn *proto.Conn
	gen  uint64
	dead chan struct{}
}

// Attach binds a freshly handshaked link to the session, aligns offsets with
// the peer, replays everything the peer has not acknowledged, and then serves
// the link until it dies. It returns the link's failure cause; the session
// itself stays alive unless the error wraps ErrSessionClosed.
func (s *Session) Attach(conn *proto.Conn, peerWindow uint64, peerStates []proto.StreamState) error {
	if peerWindow == 0 {
		peerWindow = proto.DefaultWindow
	}
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		_ = conn.Close()
		return err
	}
	s.linkGen++
	ls := &linkState{conn: conn, gen: s.linkGen, dead: make(chan struct{})}
	superseded := s.link // a reconnect may race with a link we still believe in
	s.link = conn
	s.peerWindow = peerWindow
	if err := s.alignLocked(peerStates); err != nil {
		s.link = nil
		s.mu.Unlock()
		perr := &ProtocolError{err: err}
		s.terminate(perr)
		return perr
	}
	replay := s.pendingReplayLocked()
	s.cond.Broadcast()
	s.notifyLocked()
	s.mu.Unlock()

	if superseded != nil {
		// Tear down the old link so its loops cannot interleave frames with
		// the new one. They observe the generation change and exit.
		_ = superseded.Close()
	}

	s.log.Info("link attached", "gen", ls.gen, "streams", len(peerStates), "replay_bytes", replay)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if err := s.writeLoop(ls); err != nil {
			s.log.Debug("writer stopped", "gen", ls.gen, "err", err)
		}
	}()

	readErr := s.readLoop(ls)
	close(ls.dead)
	_ = conn.Close()
	<-writerDone

	s.mu.Lock()
	if s.linkGen == ls.gen {
		s.link = nil
		s.detachedAt = time.Now()
	}
	s.cond.Broadcast()
	closeErr := s.closeErr
	s.mu.Unlock()

	if closeErr != nil {
		return closeErr
	}
	s.log.Info("link detached", "gen", ls.gen, "err", readErr)
	return readErr
}

// pendingReplayLocked counts the bytes that will be replayed on this link.
func (s *Session) pendingReplayLocked() uint64 {
	var n uint64
	for _, st := range s.streams {
		n += st.send.end() - st.send.cursor
	}
	return n
}

// alignLocked implements the resumption rules of docs/02-wire-protocol.md §3.
func (s *Session) alignLocked(peerStates []proto.StreamState) error {
	peer := make(map[uint64]proto.StreamState, len(peerStates))
	for _, ps := range peerStates {
		peer[ps.StreamID] = ps
	}
	for _, id := range append([]uint64(nil), s.order...) {
		st := s.streams[id]
		ps, ok := peer[id]
		if !ok {
			if s.role == RoleClient {
				// The server never saw this stream (its OPEN was lost with the
				// previous link). Nothing can have been acknowledged.
				if st.send.base != 0 {
					return fmt.Errorf("stream %d: peer forgot a stream with %d acknowledged bytes", id, st.send.base)
				}
				st.needsOpen = true
				st.send.cursor = 0
				st.sendLimit = s.peerWindow
				if st.finSent {
					st.finQueued = true
				}
				continue
			}
			// The client dropped this stream; retire it locally.
			st.failLocked(fmt.Errorf("%w: stream abandoned by peer", ErrSessionClosed))
			s.removeStreamLocked(id)
			continue
		}
		if err := st.send.rewind(ps.RecvOffset); err != nil {
			return fmt.Errorf("stream %d: %w", id, err)
		}
		st.sendLimit = ps.ReadOffset + s.peerWindow
		st.needsOpen = false
		if st.finSent && ps.Flags&proto.FlagFinReceived == 0 {
			st.finQueued = true // the FIN itself was lost; resend after replay
		}
		// Our ACK state is link-local: re-advertise on the new link.
		st.ackedRecv = 0
		st.ackedRead = 0
		delete(peer, id)
	}
	// Streams the peer believes in but we do not know. The client is
	// authoritative about which streams exist: a server that has not seen the
	// OPEN yet simply waits for the client to replay it, while a client tells
	// the server to drop a stream it has already retired.
	if s.role == RoleClient {
		for id := range peer {
			s.enqueueLocked(&proto.Reset{StreamID: id, Code: proto.CodeSessionUnknown})
		}
	}
	return nil
}

// DropLink closes the current link, forcing the client supervisor to build a
// new one. It reports whether a link was attached.
//
// Tests use it for fault injection; a client can also use it when the OS
// reports that the default route changed, to re-home the session immediately
// instead of waiting for the old path to time out.
func (s *Session) DropLink() bool {
	s.mu.Lock()
	link := s.link
	s.mu.Unlock()
	if link == nil {
		return false
	}
	_ = link.Close()
	return true
}
