package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// Tuning holds the QUIC knobs we expose. Defaults favour fast detection of a
// dead path over minimal traffic: the session layer reconnects cheaply, so
// noticing quickly is worth more than saving keepalive packets.
type Tuning struct {
	KeepAlive   time.Duration
	IdleTimeout time.Duration
	Window      uint64
}

func (t Tuning) withDefaults() Tuning {
	if t.KeepAlive == 0 {
		t.KeepAlive = 5 * time.Second
	}
	if t.IdleTimeout == 0 {
		t.IdleTimeout = 20 * time.Second
	}
	if t.Window == 0 {
		t.Window = proto.DefaultWindow
	}
	return t
}

// QUICConfig builds the quic-go configuration used by both ends.
func QUICConfig(t Tuning) *quic.Config {
	t = t.withDefaults()
	// Give QUIC's own flow control room above our session window so the
	// session layer, not QUIC, is the binding constraint.
	streamWindow := t.Window * 2
	return &quic.Config{
		KeepAlivePeriod:                t.KeepAlive,
		MaxIdleTimeout:                 t.IdleTimeout,
		HandshakeIdleTimeout:           10 * time.Second,
		MaxIncomingStreams:             4,
		MaxIncomingUniStreams:          -1,
		InitialStreamReceiveWindow:     streamWindow,
		MaxStreamReceiveWindow:         streamWindow * 4,
		InitialConnectionReceiveWindow: streamWindow,
		MaxConnectionReceiveWindow:     streamWindow * 8,
	}
}

// Link is the tunnel stream plus the QUIC connection carrying it.
//
// Close tears down the whole connection, not just the stream: a QUIC
// stream Close only shuts the sending half, which would leave a superseded
// link's reader blocked forever.
type Link struct {
	conn   *quic.Conn
	stream *quic.Stream
}

// NewLink pairs an accepted tunnel stream with its connection.
func NewLink(conn *quic.Conn, stream *quic.Stream) *Link {
	return &Link{conn: conn, stream: stream}
}

func (l *Link) Read(p []byte) (int, error)  { return l.stream.Read(p) }
func (l *Link) Write(p []byte) (int, error) { return l.stream.Write(p) }

// SetReadDeadline bounds handshake reads.
func (l *Link) SetReadDeadline(t time.Time) error { return l.stream.SetReadDeadline(t) }

// RemoteAddr reports the peer's current address, which changes when the
// connection migrates.
func (l *Link) RemoteAddr() net.Addr { return l.conn.RemoteAddr() }

// ExporterLabel is the RFC 8446 §7.5 exporter label for the HELLO binding
// message. Both ends derive the same 32 bytes from the TLS master secret; a
// man in the middle ends up with two different values, one per side.
const ExporterLabel = "EXPORTER-tingly-shell-hello"

// Exporter returns this connection's channel binding value.
func (l *Link) Exporter() ([]byte, error) {
	return Exporter(l.conn)
}

// Exporter derives the channel binding value of a QUIC connection.
func Exporter(conn *quic.Conn) ([]byte, error) {
	state := conn.ConnectionState().TLS
	out, err := state.ExportKeyingMaterial(ExporterLabel, nil, 32)
	if err != nil {
		return nil, fmt.Errorf("transport: export keying material: %w", err)
	}
	return out, nil
}

// PeerPin returns the SPKI pin of the certificate the server presented, or ""
// if there was none.
func (l *Link) PeerPin() string {
	certs := l.conn.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	return Pin(certs[0])
}

// Close shuts the stream and the connection underneath it.
func (l *Link) Close() error {
	l.stream.CancelRead(0)
	return l.conn.CloseWithError(0, "link closed")
}

// CloseGracefully lets already written frames reach the peer before the
// connection goes away.
//
// Closing a QUIC connection sends CONNECTION_CLOSE immediately and discards
// whatever is still queued on its streams, so a plain Close would swallow the
// last frame: a refusing HELLO_ACK, or a CLOSE telling the peer not to try to
// resume. Finishing the stream first and waiting briefly for the peer to hang
// up keeps those frames meaningful.
func (l *Link) CloseGracefully(wait time.Duration) error {
	_ = l.stream.Close() // FIN: everything written so far is delivered
	select {
	case <-l.conn.Context().Done():
	case <-time.After(wait):
	}
	l.stream.CancelRead(0)
	return l.conn.CloseWithError(0, "link closed")
}

// Dial opens a QUIC connection and its single bidirectional tunnel stream.
func Dial(ctx context.Context, addr string, tlsConf *tls.Config, t Tuning) (*Link, error) {
	conn, err := quic.DialAddr(ctx, addr, tlsConf, QUICConfig(t))
	if err != nil {
		return nil, fmt.Errorf("transport: dial %s: %w", addr, err)
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		conn.CloseWithError(0, "open tunnel stream")
		return nil, fmt.Errorf("transport: open tunnel stream: %w", err)
	}
	// The server only learns about the stream once bytes arrive, and the
	// client's first frame is HELLO, so nothing extra is needed here.
	return NewLink(conn, stream), nil
}

// Listen starts a QUIC listener for the tunnel.
func Listen(addr string, tlsConf *tls.Config, t Tuning) (*quic.Listener, error) {
	ln, err := quic.ListenAddr(addr, tlsConf, QUICConfig(t))
	if err != nil {
		return nil, fmt.Errorf("transport: listen %s: %w", addr, err)
	}
	return ln, nil
}
