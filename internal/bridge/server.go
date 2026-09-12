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

// ServerConfig configures the sshd-side bridge.
type ServerConfig struct {
	Listen     string   // UDP address to listen on
	Targets    []string // allowed TCP targets; the first one is the default
	Token      []byte   // pre-shared token
	Window     uint64
	MaxStreams int
	Linger     time.Duration // how long a session survives without a link
	Tuning     transport.Tuning
	TLS        *tls.Config
	Logger     *slog.Logger
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
	if len(cfg.Targets) == 0 {
		return nil, errors.New("bridge: server needs at least one target")
	}
	ln, err := transport.Listen(cfg.Listen, cfg.TLS, cfg.Tuning)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: cfg.Logger, ln: ln, sessions: make(map[proto.SessionID]*mux.Session)}, nil
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
		_ = pc.WriteAndFlush(&proto.HelloAck{Code: proto.CodeProtocol, Reason: "expected HELLO"})
		_ = pc.Close()
		return errors.New("bridge: first frame was not HELLO")
	}
	if err := link.SetReadDeadline(time.Time{}); err != nil {
		return err
	}

	sess, err := s.resolveSession(ctx, hello)
	if err != nil {
		var he *HandshakeError
		if errors.As(err, &he) {
			_ = pc.WriteAndFlush(&proto.HelloAck{Code: he.Code, Reason: he.Reason})
		}
		_ = pc.Close()
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
