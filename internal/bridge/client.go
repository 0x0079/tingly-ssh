package bridge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync/atomic"
	"time"

	"github.com/0x0079/tingly-shell/internal/auth"
	"github.com/0x0079/tingly-shell/internal/mux"
	"github.com/0x0079/tingly-shell/internal/proto"
	"github.com/0x0079/tingly-shell/internal/transport"
)

// Reconnect backoff bounds (docs/03-session-resumption.md §3).
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 15 * time.Second
	dialTimeout = 15 * time.Second
)

// ClientConfig configures the ssh-side bridge.
type ClientConfig struct {
	Server string // QUIC server address
	Target string // target hint sent in OPEN
	Token  []byte
	// Keys authenticates with SSH keys instead of Token
	// (.design/ssh-key-auth.pencil.md). Exactly one of the two is set.
	Keys       KeyProver
	Window     uint64
	MaxStreams int
	Linger     time.Duration // give up on the session after this long with no link
	Tuning     transport.Tuning
	TLS        *tls.Config
	Logger     *slog.Logger
}

// KeyProver signs a HELLO binding message with the user's SSH keys.
// auth.Prover is the real one.
type KeyProver interface {
	Prove(message []byte) ([]proto.KeyProof, error)
}

// Client owns one resumable session and the supervisor that keeps a link under it.
type Client struct {
	cfg      ClientConfig
	log      *slog.Logger
	sess     *mux.Session
	epoch    atomic.Uint64
	resuming atomic.Bool // set once the server has acknowledged this session

	// ticket lets a key-authenticated session reconnect without signing
	// again; ticketPin is the server key it was issued by, and the only one
	// it is ever shown to. Both are touched only by the Run goroutine.
	ticket    []byte
	ticketPin string
}

// NewClient creates the session. Run drives the reconnect loop; the Serve
// methods feed it local connections.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Linger == 0 {
		cfg.Linger = 60 * time.Second
	}
	if (cfg.Keys == nil) == (len(cfg.Token) == 0) {
		return nil, errors.New("bridge: client needs exactly one of a token or SSH keys")
	}
	id, err := proto.NewSessionID()
	if err != nil {
		return nil, err
	}
	sess := mux.New(mux.Config{
		Role:       mux.RoleClient,
		SessionID:  id,
		Window:     cfg.Window,
		MaxStreams: cfg.MaxStreams,
		Logger:     cfg.Logger,
	})
	return &Client{cfg: cfg, log: cfg.Logger.With("session", id.Short()), sess: sess}, nil
}

// Session exposes the underlying session (used by tests and the CLI).
func (c *Client) Session() *mux.Session { return c.sess }

// Run keeps a link attached to the session until the session ends or ctx is
// cancelled. A dead link is an ordinary event here, not an error.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	downSince := time.Now()

	// Attach blocks until its link dies, so cancellation has to reach the
	// session rather than waiting for the loop to come back around. Without
	// this, Ctrl-C on a healthy connection would hang until the link failed.
	go func() {
		select {
		case <-ctx.Done():
			c.sess.Close(proto.CodeShutdown, "client shutting down")
		case <-c.sess.Done():
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			c.sess.Close(proto.CodeShutdown, "client shutting down")
			return err
		}
		select {
		case <-c.sess.Done():
			return c.sess.Err()
		default:
		}

		pc, window, states, err := c.connect(ctx)
		if err != nil {
			var he *HandshakeError
			if errors.As(err, &he) && he.Terminal() {
				c.log.Error("session cannot be resumed", "err", err)
				c.sess.Close(he.Code, he.Reason)
				return err
			}
			// A known server presenting another key is what an attack looks
			// like; like ssh, stop rather than keep knocking.
			if errors.Is(err, transport.ErrServerKeyChanged) {
				c.log.Error("server key changed, refusing to connect", "err", err)
				c.sess.Close(proto.CodeShutdown, "server key changed")
				return err
			}
			if over := time.Since(downSince); over > c.cfg.Linger {
				c.log.Error("giving up on session", "down_for", over.Round(time.Second), "err", err)
				c.sess.Close(proto.CodeTimeout, "reconnect window exhausted")
				return fmt.Errorf("bridge: no link for %s: %w", over.Round(time.Second), err)
			}
			delay := backoff(attempt)
			attempt++
			c.log.Warn("reconnect failed", "attempt", attempt, "retry_in", delay.Round(time.Millisecond), "err", err)
			select {
			case <-ctx.Done():
				continue
			case <-c.sess.Done():
				continue
			case <-time.After(delay):
			}
			continue
		}

		attempt = 0
		err = c.sess.Attach(pc, window, states)
		downSince = time.Now()
		if errors.Is(err, mux.ErrSessionClosed) {
			return err
		}
		c.log.Warn("link lost, reconnecting", "err", err)
	}
}

// connect dials a new link and completes the handshake.
func (c *Client) connect(ctx context.Context) (*proto.Conn, uint64, []proto.StreamState, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	link, err := transport.Dial(dialCtx, c.cfg.Server, c.cfg.TLS, c.cfg.Tuning)
	if err != nil {
		return nil, 0, nil, err
	}
	pc := proto.NewConn(link)
	if err := link.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		_ = pc.Close()
		return nil, 0, nil, err
	}
	hello := &proto.Hello{
		Version:   proto.Version,
		SessionID: c.sess.ID(),
		Epoch:     c.epoch.Add(1),
		Window:    c.sess.Window(),
		Token:     c.cfg.Token,
		States:    c.sess.States(),
	}
	if c.resuming.Load() {
		hello.Flags |= proto.FlagResume
	}
	method := "token"
	serverPin := link.PeerPin()
	if c.cfg.Keys != nil {
		if method, err = c.proveKeys(link, hello, serverPin); err != nil {
			_ = pc.Close()
			return nil, 0, nil, err
		}
	}
	ack, err := clientHandshake(pc, hello)
	if err != nil {
		_ = pc.Close()
		return nil, 0, nil, err
	}
	// From here on the server holds state for this session id, so later
	// handshakes must declare themselves as resumptions.
	c.resuming.Store(true)
	if len(ack.Ticket) > 0 {
		c.ticket, c.ticketPin = ack.Ticket, serverPin
	}
	if err := link.SetReadDeadline(time.Time{}); err != nil {
		_ = pc.Close()
		return nil, 0, nil, err
	}
	c.log.Info("link established", "server", link.RemoteAddr().String(), "epoch", hello.Epoch, "auth", method)
	return pc, ack.Window, ack.States, nil
}

// proveKeys fills in the key authentication fields of hello. A reconnect
// shows the resume ticket instead of asking the keys to sign again, which is
// what keeps a FIDO key from needing a touch on every network change; but
// only to the server key that issued the ticket. A signature is bound to this
// one connection and is safe to show anyone, a ticket is not.
func (c *Client) proveKeys(link *transport.Link, hello *proto.Hello, serverPin string) (string, error) {
	hello.Flags |= proto.FlagKeyAuth
	if c.ticket != nil && serverPin != "" && serverPin == c.ticketPin && c.resuming.Load() {
		hello.Ticket = c.ticket
		return "ticket", nil
	}
	exporter, err := link.Exporter()
	if err != nil {
		return "", err
	}
	proofs, err := c.cfg.Keys.Prove(auth.HelloMessage(exporter, hello.SessionID))
	if err != nil {
		return "", err
	}
	hello.Proofs = proofs
	return "ssh-key", nil
}

// ServeListener bridges every accepted TCP connection over its own stream.
func (c *Client) ServeListener(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			if err := c.bridge(ctx, conn.(*net.TCPConn)); err != nil {
				c.log.Warn("local connection ended", "err", err)
			}
		}()
	}
}

// ServeStdio bridges the process's stdin/stdout over a single stream, which is
// what `ssh -o ProxyCommand=...` needs.
func (c *Client) ServeStdio(ctx context.Context) error {
	st, err := c.sess.Open(ctx, c.cfg.Target)
	if err != nil {
		return err
	}
	return Pipe(st, Stdio())
}

func (c *Client) bridge(ctx context.Context, conn *net.TCPConn) error {
	st, err := c.sess.Open(ctx, c.cfg.Target)
	if err != nil {
		_ = conn.Close()
		return err
	}
	c.log.Info("local connection bridged", "stream", st.ID(), "from", conn.RemoteAddr().String())
	return Pipe(st, conn)
}

// backoff grows exponentially to backoffMax with +/-20% jitter so a fleet of
// clients does not reconnect in lockstep.
func backoff(attempt int) time.Duration {
	d := backoffBase << min(attempt, 6)
	if d > backoffMax {
		d = backoffMax
	}
	jitter := 1 + (rand.Float64()-0.5)*0.4
	return time.Duration(float64(d) * jitter)
}
