package proto

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go/quicvarint"
)

// Conn is a framed codec over a reliable ordered byte stream (in production:
// one bidirectional QUIC stream; in tests: net.Pipe).
//
// ReadFrame must be called from a single goroutine, and WriteFrame/Flush from
// a single (possibly different) goroutine. That matches the session's
// readLoop/writeLoop split.
type Conn struct {
	rwc io.ReadWriteCloser
	br  *bufio.Reader
	bw  *bufio.Writer
	buf []byte // reusable read scratch
	out []byte // reusable write scratch
}

// NewConn wraps rwc with frame buffering.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	return &Conn{
		rwc: rwc,
		br:  bufio.NewReaderSize(rwc, 64<<10),
		bw:  bufio.NewWriterSize(rwc, 64<<10),
	}
}

// WriteFrame buffers f. Call Flush to push it to the wire.
func (c *Conn) WriteFrame(f Frame) error {
	out, err := AppendFrame(c.out[:0], f)
	if err != nil {
		return err
	}
	c.out = out
	if _, err := c.bw.Write(out); err != nil {
		return fmt.Errorf("write %s: %w", f.Type(), err)
	}
	return nil
}

// Flush pushes buffered frames to the underlying stream.
func (c *Conn) Flush() error { return c.bw.Flush() }

// WriteAndFlush is the convenience path for handshake frames.
func (c *Conn) WriteAndFlush(f Frame) error {
	if err := c.WriteFrame(f); err != nil {
		return err
	}
	return c.Flush()
}

// ReadFrame reads the next frame. The returned frame does not alias internal
// buffers, so it stays valid after the next call.
func (c *Conn) ReadFrame() (Frame, error) {
	n, err := quicvarint.Read(c.br)
	if err != nil {
		return nil, err // io.EOF and link errors pass through unwrapped
	}
	if n == 0 || n > MaxFrameSize {
		return nil, fmt.Errorf("%w: frame length %d out of range (1..%d)", ErrMalformed, n, MaxFrameSize)
	}
	if uint64(cap(c.buf)) < n {
		c.buf = make([]byte, n)
	}
	body := c.buf[:n]
	if _, err := io.ReadFull(c.br, body); err != nil {
		return nil, err
	}
	return ParseFrame(body)
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.rwc.Close() }

// gracefulCloser is implemented by transports that can flush what is already
// written before tearing the connection down.
type gracefulCloser interface {
	CloseGracefully(time.Duration) error
}

// CloseGracefully gives frames already written a chance to arrive. Transports
// that cannot do better fall back to Close.
func (c *Conn) CloseGracefully(wait time.Duration) error {
	if g, ok := c.rwc.(gracefulCloser); ok {
		return g.CloseGracefully(wait)
	}
	return c.rwc.Close()
}
