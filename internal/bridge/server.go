package bridge

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/0x0079/tingly-shell/internal/mux"
	"github.com/0x0079/tingly-shell/internal/proto"
	"github.com/0x0079/tingly-shell/internal/transport"
)

// handshakeTimeout bounds how long a new connection may take to send HELLO.
const handshakeTimeout = 10 * time.Second

// DefaultMaxSessions bounds how many sessions one server holds at once.
//
// Memory is the reason the bound exists: a session retains up to
// MaxStreams * (send window + receive window) bytes, and it keeps holding them
// for the whole linger window after its link dies. Without a cap, anyone with
// a valid token can grow the server's footprint at will (risk R-3 in
// docs/04-security-model.md §8).
const DefaultMaxSessions = 128

// WorstCaseMemory reports the upper bound on session memory for a config, so
// the operator can size the host instead of guessing.
func (c ServerConfig) WorstCaseMemory() uint64 {
	window := c.Window
	if window == 0 {
		window = proto.DefaultWindow
	}
	streams := c.MaxStreams
	if streams == 0 {
		streams = 64
	}
	sessions := c.MaxSessions
	if sessions == 0 {
		sessions = DefaultMaxSessions
	}
	// Each stream holds a replay buffer and a receive buffer.
	return uint64(sessions) * uint64(streams) * window * 2
}

// ServerConfig configures the sshd-side bridge.
type ServerConfig struct {
	Listen      string   // UDP address to listen on
	Targets     []string // allowed TCP targets; the first one is the default
	Token       []byte   // pre-shared token
	Window      uint64
	MaxStreams  int
	MaxSessions int           // concurrent sessions this server will hold
	Linger      time.Duration // how long a session survives without a link
	Tuning      transport.Tuning
	TLS         *tls.Config
	Logger      *slog.Logger
}

// Server accepts QUIC links, resumes or creates sessions, and bridges each
// logical stream to a TCP target.
type Server struct {
	cfg ServerConfig
	log *slog.Logger
	ln  *quic.Listener

	mu       sync.Mutex
	sessions map[proto.SessionID]*mux.Session
}

// NewServer binds the QUIC listener.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Linger == 0 {
		cfg.Linger = 60 * time.Second
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if len(cfg.Targets) == 0 {
		return nil, errors.New("bridge: server needs at least one target")
	}
	ln, err := transport.Listen(cfg.Listen, cfg.TLS, cfg.Tuning)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: cfg.Logger, ln: ln, sessions: make(map[proto.SessionID]*mux.Session)}, nil
}

// sessionCount and linkedCount expose registry state for tests.
func (s *Server) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) linkedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.Linked() {
			n++
		}
	}
	return n
}

// Addr reports the bound UDP address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops accepting and terminates every live session.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	sessions := make([]*mux.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.Close(proto.CodeShutdown, "server shutting down")
	}
	return err
}

// Serve accepts connections until ctx is cancelled or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	go s.reap(ctx)
	for {
		conn, err := s.ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, quic.ErrServerClosed) {
				return nil
			}
			return err
		}
		go func() {
			if err := s.handleConn(ctx, conn); err != nil {
				s.log.Debug("link ended", "remote", conn.RemoteAddr().String(), "err", err)
			}
		}()
	}
}

// reap terminates sessions that never came back within the linger window.
func (s *Server) reap(ctx context.Context) {
	interval := s.cfg.Linger / 4
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			var stale []*mux.Session
			for _, sess := range s.sessions {
				if !sess.Linked() && time.Since(sess.DetachedSince()) > s.cfg.Linger {
					stale = append(stale, sess)
				}
			}
			s.mu.Unlock()
			for _, sess := range stale {
				s.log.Info("session expired", "session", sess.ID().Short(), "linger", s.cfg.Linger)
				sess.Close(proto.CodeTimeout, "session linger expired")
			}
		}
	}
}

func (s *Server) handleConn(ctx context.Context, conn *quic.Conn) error {
	acceptCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	stream, err := conn.AcceptStream(acceptCtx)
	if err != nil {
		conn.CloseWithError(quic.ApplicationErrorCode(proto.CodeProtocol), "no tunnel stream")
		return err
	}
	link := transport.NewLink(conn, stream)
	pc := proto.NewConn(link)

	if err := link.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	f, err := pc.ReadFrame()
	if err != nil {
		_ = pc.Close()
		return err
	}
	hello, ok := f.(*proto.Hello)
	if !ok {
		refuse(pc, &proto.HelloAck{Code: proto.CodeProtocol, Reason: "expected HELLO"})
		return errors.New("bridge: first frame was not HELLO")
	}
	if err := link.SetReadDeadline(time.Time{}); err != nil {
		return err
	}

	sess, err := s.resolveSession(ctx, hello)
	if err != nil {
		var he *HandshakeError
		if errors.As(err, &he) {
			refuse(pc, &proto.HelloAck{Code: he.Code, Reason: he.Reason})
		} else {
			_ = pc.Close()
		}
		return err
	}

	ack := &proto.HelloAck{Code: proto.CodeOK, Window: sess.Window(), States: sess.States()}
	if err := pc.WriteAndFlush(ack); err != nil {
		_ = pc.Close()
		return err
	}
	s.log.Info("link accepted",
		"session", hello.SessionID.Short(), "epoch", hello.Epoch,
		"remote", conn.RemoteAddr().String(), "streams", len(hello.States))
	return sess.Attach(pc, hello.Window, hello.States)
}

// refuse answers a handshake with a reason and closes the link gracefully, so
// the client can tell "your token is wrong" from "the network broke" and stop
// retrying.
func refuse(pc *proto.Conn, ack *proto.HelloAck) {
	_ = pc.WriteAndFlush(ack)
	_ = pc.CloseGracefully(refusalFlushTimeout)
}

// refusalFlushTimeout bounds how long a refused link waits for the client to
// read the reason before the connection is torn down.
const refusalFlushTimeout = 2 * time.Second

// resolveSession authenticates the HELLO and returns the session it belongs
// to, creating one if this is a fresh session.
func (s *Server) resolveSession(ctx context.Context, hello *proto.Hello) (*mux.Session, error) {
	if hello.Version != proto.Version {
		return nil, &HandshakeError{Code: proto.CodeUnsupportedVersion, Reason: "unsupported protocol version"}
	}
	if subtle.ConstantTimeCompare(hello.Token, s.cfg.Token) != 1 {
		return nil, &HandshakeError{Code: proto.CodeUnauthorized, Reason: "invalid token"}
	}

	s.mu.Lock()
	sess, known := s.sessions[hello.SessionID]
	if !known {
		if len(s.sessions) >= s.cfg.MaxSessions && !s.evictOneDetachedLocked() {
			s.mu.Unlock()
			s.log.Warn("refused a new session at the limit",
				"sessions", s.cfg.MaxSessions, "remote_session", hello.SessionID.Short())
			return nil, &HandshakeError{Code: proto.CodeResourceExhausted, Reason: "session limit reached"}
		}
		if hello.Flags&proto.FlagResume != 0 {
			s.mu.Unlock()
			// The client wants to resume a session we no longer hold (most
			// often because the server restarted).
			return nil, &HandshakeError{Code: proto.CodeSessionUnknown, Reason: "unknown session"}
		}
		sess = mux.New(mux.Config{
			Role:       mux.RoleServer,
			SessionID:  hello.SessionID,
			Window:     s.cfg.Window,
			MaxStreams: s.cfg.MaxStreams,
			Logger:     s.cfg.Logger,
		})
		s.sessions[hello.SessionID] = sess
		s.mu.Unlock()

		go s.serveStreams(ctx, sess)
		go func() {
			<-sess.Done()
			s.mu.Lock()
			if cur := s.sessions[hello.SessionID]; cur == sess {
				delete(s.sessions, hello.SessionID)
			}
			s.mu.Unlock()
			s.log.Info("session closed", "session", hello.SessionID.Short(), "err", sess.Err())
		}()
	} else {
		s.mu.Unlock()
	}

	if !sess.AdoptEpoch(hello.Epoch) {
		return nil, &HandshakeError{Code: proto.CodeEpochStale, Reason: "a newer link already owns this session"}
	}
	return sess, nil
}

// evictOneDetachedLocked frees a slot by closing the session that has been
// without a link the longest, and reports whether it found one. A client whose
// process died leaves a session lingering; that zombie should not keep the
// same client from reconnecting. Sessions with a live link are never evicted.
func (s *Server) evictOneDetachedLocked() bool {
	var victim *mux.Session
	var oldest time.Time
	for _, sess := range s.sessions {
		if sess.Linked() {
			continue
		}
		if at := sess.DetachedSince(); victim == nil || at.Before(oldest) {
			victim, oldest = sess, at
		}
	}
	if victim == nil {
		return false
	}
	s.log.Info("evicting an idle session to make room",
		"session", victim.ID().Short(), "detached_for", time.Since(oldest).Round(time.Second))
	// Close outside the lock would be cleaner, but terminate only takes the
	// session's own lock, never the server's.
	go victim.Close(proto.CodeTimeout, "evicted to make room for a new session")
	delete(s.sessions, victim.ID())
	return true
}

// serveStreams dials the target for every logical stream the client opens.
func (s *Server) serveStreams(ctx context.Context, sess *mux.Session) {
	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		go s.bridgeStream(ctx, sess, st)
	}
}

func (s *Server) bridgeStream(ctx context.Context, sess *mux.Session, st *mux.Stream) {
	target := s.resolveTarget(st.Target())
	if target == "" {
		s.log.Warn("rejected stream target", "session", sess.ID().Short(), "stream", st.ID(), "target", st.Target())
		_ = st.Close()
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", target)
	if err != nil {
		s.log.Warn("dial target failed", "target", target, "err", err)
		_ = st.Close()
		return
	}
	s.log.Info("stream bridged", "session", sess.ID().Short(), "stream", st.ID(), "target", target)
	if err := Pipe(st, conn.(*net.TCPConn)); err != nil {
		s.log.Debug("stream ended", "session", sess.ID().Short(), "stream", st.ID(), "err", err)
	}
}

// resolveTarget applies the target allowlist. With a single configured target
// the client's hint is ignored entirely, which keeps the server from becoming
// an open relay (docs/04-security-model.md §2).
func (s *Server) resolveTarget(hint string) string {
	if len(s.cfg.Targets) == 1 {
		return s.cfg.Targets[0]
	}
	if slices.Contains(s.cfg.Targets, hint) {
		return hint
	}
	return ""
}
