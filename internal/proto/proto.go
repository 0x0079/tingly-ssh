// Package proto implements the tingly/0 wire format described in
// docs/02-wire-protocol.md. It is pure encoding/decoding: no I/O policy,
// no session state.
package proto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	// ALPN is the TLS application protocol negotiated on the QUIC connection.
	ALPN = "tingly/0"

	// Version is PROTOCOL_VERSION from docs/02-wire-protocol.md.
	Version = 1

	// MaxFrameSize bounds a single encoded frame (type + payload).
	MaxFrameSize = 1 << 20

	// MaxDataPayload is the largest DATA payload a sender may emit; larger
	// writes are split across frames.
	MaxDataPayload = 64 << 10

	// DefaultWindow is the default per-stream receive window.
	DefaultWindow = 1 << 20
)

// FrameType identifies a frame on the wire.
type FrameType uint64

const (
	TypeHello    FrameType = 0x01
	TypeHelloAck FrameType = 0x02
	TypeOpen     FrameType = 0x03
	TypeData     FrameType = 0x04
	TypeAck      FrameType = 0x05
	TypeFin      FrameType = 0x06
	TypeReset    FrameType = 0x07
	TypePing     FrameType = 0x08
	TypePong     FrameType = 0x09
	TypeClose    FrameType = 0x0a
)

func (t FrameType) String() string {
	switch t {
	case TypeHello:
		return "HELLO"
	case TypeHelloAck:
		return "HELLO_ACK"
	case TypeOpen:
		return "OPEN"
	case TypeData:
		return "DATA"
	case TypeAck:
		return "ACK"
	case TypeFin:
		return "FIN"
	case TypeReset:
		return "RESET"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	case TypeClose:
		return "CLOSE"
	default:
		return fmt.Sprintf("UNKNOWN(0x%x)", uint64(t))
	}
}

// HELLO_ACK / CLOSE result codes.
const (
	CodeOK                 uint64 = 0
	CodeUnsupportedVersion uint64 = 1
	CodeUnauthorized       uint64 = 2
	CodeSessionUnknown     uint64 = 3
	CodeEpochStale         uint64 = 4
	CodeInternal           uint64 = 5
	CodeProtocol           uint64 = 6
	CodeShutdown           uint64 = 7
	CodeTimeout            uint64 = 8
	// CodeResourceExhausted refuses a new session because the server is at its
	// configured limit. It is retryable: the client backs off and tries again.
	CodeResourceExhausted uint64 = 9
)

// CodeName renders a result code for logs and errors.
func CodeName(code uint64) string {
	switch code {
	case CodeOK:
		return "ok"
	case CodeUnsupportedVersion:
		return "unsupported_version"
	case CodeUnauthorized:
		return "unauthorized"
	case CodeSessionUnknown:
		return "session_unknown"
	case CodeEpochStale:
		return "epoch_stale"
	case CodeInternal:
		return "internal"
	case CodeProtocol:
		return "protocol"
	case CodeShutdown:
		return "shutdown"
	case CodeTimeout:
		return "timeout"
	case CodeResourceExhausted:
		return "resource_exhausted"
	default:
		return fmt.Sprintf("code(%d)", code)
	}
}

// Terminal reports whether a HELLO_ACK code means the client must stop
// retrying this session.
func Terminal(code uint64) bool {
	switch code {
	case CodeUnsupportedVersion, CodeUnauthorized, CodeSessionUnknown, CodeShutdown:
		return true
	default:
		return false
	}
}

// SessionID identifies a resumable session.
type SessionID [16]byte

// NewSessionID draws a session ID from the system CSPRNG.
func NewSessionID() (SessionID, error) {
	var id SessionID
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("generate session id: %w", err)
	}
	return id, nil
}

// String returns the full hex encoding.
func (id SessionID) String() string { return hex.EncodeToString(id[:]) }

// Short returns the first 8 hex characters, which is what logs print
// (docs/04-security-model.md §5).
func (id SessionID) Short() string { return hex.EncodeToString(id[:4]) }

// HELLO flags.
const (
	// FlagResume says the client has already been attached on this session, so
	// the server is expected to know it. Without the flag an unknown session
	// id is simply a new session, even when the HELLO already carries streams
	// the application opened before the first link existed.
	FlagResume uint64 = 1 << 0
)

// Stream state flags exchanged during resumption.
const (
	FlagFinReceived uint64 = 1 << 0
	FlagFinSent     uint64 = 1 << 1
)

// StreamState is one side's view of a logical stream, used to align offsets
// when a new link attaches (docs/02-wire-protocol.md §3).
type StreamState struct {
	StreamID   uint64
	RecvOffset uint64
	ReadOffset uint64
	Flags      uint64
	Target     string
}
