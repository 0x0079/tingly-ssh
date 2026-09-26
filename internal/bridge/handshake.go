package bridge

import (
	"fmt"

	"github.com/0x0079/tingly-ssh/internal/proto"
)

// HandshakeError reports a HELLO_ACK that refused the link.
type HandshakeError struct {
	Code   uint64
	Reason string
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("bridge: handshake refused (%s): %s", proto.CodeName(e.Code), e.Reason)
}

// Terminal reports whether retrying this session is pointless.
func (e *HandshakeError) Terminal() bool { return proto.Terminal(e.Code) }

// clientHandshake sends HELLO and waits for HELLO_ACK. The returned ack
// carries the server's window and stream states, which Attach aligns against.
func clientHandshake(conn *proto.Conn, hello *proto.Hello) (*proto.HelloAck, error) {
	if err := conn.WriteAndFlush(hello); err != nil {
		return nil, fmt.Errorf("bridge: send HELLO: %w", err)
	}
	f, err := conn.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("bridge: await HELLO_ACK: %w", err)
	}
	ack, ok := f.(*proto.HelloAck)
	if !ok {
		return nil, fmt.Errorf("bridge: expected HELLO_ACK, got %s", f.Type())
	}
	if ack.Code != proto.CodeOK {
		return nil, &HandshakeError{Code: ack.Code, Reason: ack.Reason}
	}
	return ack, nil
}
