package proto

import (
	"errors"
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
)

// ErrMalformed reports a frame that cannot be parsed, or that violates a
// size bound. The caller must treat it as a protocol error and tear the
// link down.
var ErrMalformed = errors.New("proto: malformed frame")

// Frame is one decoded wire frame.
type Frame interface {
	Type() FrameType
	// appendPayload appends the frame payload (everything after the type) to b.
	appendPayload(b []byte) []byte
}

// Hello is the first frame a client sends on a new link.
type Hello struct {
	Version   uint64
	SessionID SessionID
	Epoch     uint64
	Flags     uint64
	Window    uint64
	Token     []byte
	States    []StreamState
	// Ticket and Proofs are on the wire only when Flags has FlagKeyAuth.
	Ticket []byte
	Proofs []KeyProof
}

// HelloAck is the server's answer to Hello.
type HelloAck struct {
	Code   uint64
	Reason string
	Window uint64
	States []StreamState
	// Ticket is an optional trailing field: a resume ticket issued to a client
	// that authenticated with a key. It is absent from the wire when empty.
	Ticket []byte
}

// Open announces a new logical stream.
type Open struct {
	StreamID uint64
	Target   string
}

// Data carries stream bytes at an absolute offset.
type Data struct {
	StreamID uint64
	Offset   uint64
	Payload  []byte
}

// Ack reports how much the peer has received and how much the peer's
// application has consumed (the flow control credit).
type Ack struct {
	StreamID   uint64
	RecvOffset uint64
	ReadOffset uint64
}

// Fin is a graceful half-close at FinalOffset.
type Fin struct {
	StreamID    uint64
	FinalOffset uint64
}

// Reset aborts a stream in both directions.
type Reset struct {
	StreamID uint64
	Code     uint64
}

// Ping is an application-level liveness probe.
type Ping struct{ Nonce uint64 }

// Pong answers a Ping.
type Pong struct{ Nonce uint64 }

// Close terminates the session; it is not resumable.
type Close struct {
	Code   uint64
	Reason string
}

func (*Hello) Type() FrameType    { return TypeHello }
func (*HelloAck) Type() FrameType { return TypeHelloAck }
func (*Open) Type() FrameType     { return TypeOpen }
func (*Data) Type() FrameType     { return TypeData }
func (*Ack) Type() FrameType      { return TypeAck }
func (*Fin) Type() FrameType      { return TypeFin }
func (*Reset) Type() FrameType    { return TypeReset }
func (*Ping) Type() FrameType     { return TypePing }
func (*Pong) Type() FrameType     { return TypePong }
func (*Close) Type() FrameType    { return TypeClose }

func appendBytes(b []byte, v []byte) []byte {
	b = quicvarint.Append(b, uint64(len(v)))
	return append(b, v...)
}

func appendString(b []byte, s string) []byte {
	b = quicvarint.Append(b, uint64(len(s)))
	return append(b, s...)
}

func appendStates(b []byte, states []StreamState) []byte {
	b = quicvarint.Append(b, uint64(len(states)))
	for _, s := range states {
		b = quicvarint.Append(b, s.StreamID)
		b = quicvarint.Append(b, s.RecvOffset)
		b = quicvarint.Append(b, s.ReadOffset)
		b = quicvarint.Append(b, s.Flags)
		b = appendString(b, s.Target)
	}
	return b
}

func (f *Hello) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.Version)
	b = append(b, f.SessionID[:]...)
	b = quicvarint.Append(b, f.Epoch)
	b = quicvarint.Append(b, f.Flags)
	b = quicvarint.Append(b, f.Window)
	b = appendBytes(b, f.Token)
	b = appendStates(b, f.States)
	if f.Flags&FlagKeyAuth == 0 {
		return b
	}
	b = appendBytes(b, f.Ticket)
	b = quicvarint.Append(b, uint64(len(f.Proofs)))
	for _, p := range f.Proofs {
		b = appendBytes(b, p.PublicKey)
		b = appendBytes(b, p.Signature)
	}
	return b
}

func (f *HelloAck) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.Code)
	b = appendString(b, f.Reason)
	b = quicvarint.Append(b, f.Window)
	b = appendStates(b, f.States)
	if len(f.Ticket) > 0 {
		b = appendBytes(b, f.Ticket)
	}
	return b
}

func (f *Open) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.StreamID)
	return appendString(b, f.Target)
}

func (f *Data) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.StreamID)
	b = quicvarint.Append(b, f.Offset)
	return appendBytes(b, f.Payload)
}

func (f *Ack) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.StreamID)
	b = quicvarint.Append(b, f.RecvOffset)
	return quicvarint.Append(b, f.ReadOffset)
}

func (f *Fin) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.StreamID)
	return quicvarint.Append(b, f.FinalOffset)
}

func (f *Reset) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.StreamID)
	return quicvarint.Append(b, f.Code)
}

func (f *Ping) appendPayload(b []byte) []byte { return quicvarint.Append(b, f.Nonce) }
func (f *Pong) appendPayload(b []byte) []byte { return quicvarint.Append(b, f.Nonce) }

func (f *Close) appendPayload(b []byte) []byte {
	b = quicvarint.Append(b, f.Code)
	return appendString(b, f.Reason)
}

// AppendFrame encodes f as length-prefixed bytes and appends it to b.
func AppendFrame(b []byte, f Frame) ([]byte, error) {
	body := make([]byte, 0, 64)
	body = quicvarint.Append(body, uint64(f.Type()))
	body = f.appendPayload(body)
	if len(body) > MaxFrameSize {
		return nil, fmt.Errorf("%w: %s is %d bytes, limit %d", ErrMalformed, f.Type(), len(body), MaxFrameSize)
	}
	b = quicvarint.Append(b, uint64(len(body)))
	return append(b, body...), nil
}

// parser consumes a frame body.
type parser struct {
	b   []byte
	err error
}

func (p *parser) varint() uint64 {
	if p.err != nil {
		return 0
	}
	v, n, err := quicvarint.Parse(p.b)
	if err != nil {
		p.err = fmt.Errorf("%w: %v", ErrMalformed, err)
		return 0
	}
	p.b = p.b[n:]
	return v
}

func (p *parser) raw(n int) []byte {
	if p.err != nil {
		return nil
	}
	if n < 0 || n > len(p.b) {
		p.err = fmt.Errorf("%w: want %d bytes, have %d", ErrMalformed, n, len(p.b))
		return nil
	}
	out := p.b[:n]
	p.b = p.b[n:]
	return out
}

// bytesField returns a copy, so decoded frames never alias the read buffer.
func (p *parser) bytesField() []byte {
	n := p.varint()
	if p.err != nil {
		return nil
	}
	raw := p.raw(int(n))
	if p.err != nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}

func (p *parser) stringField() string {
	return string(p.raw(int(p.varint())))
}

func (p *parser) proofs() []KeyProof {
	n := p.varint()
	if p.err != nil {
		return nil
	}
	if n > MaxKeyProofs {
		p.err = fmt.Errorf("%w: %d key proofs, limit %d", ErrMalformed, n, MaxKeyProofs)
		return nil
	}
	proofs := make([]KeyProof, 0, n)
	for i := uint64(0); i < n; i++ {
		kp := KeyProof{PublicKey: p.bytesField(), Signature: p.bytesField()}
		if p.err != nil {
			return nil
		}
		proofs = append(proofs, kp)
	}
	return proofs
}

func (p *parser) states() []StreamState {
	n := p.varint()
	if p.err != nil {
		return nil
	}
	// Each state is at least 5 bytes on the wire; reject counts that cannot
	// possibly fit so a bogus varint can't make us allocate wildly.
	if n > uint64(len(p.b)) {
		p.err = fmt.Errorf("%w: state count %d exceeds remaining %d bytes", ErrMalformed, n, len(p.b))
		return nil
	}
	states := make([]StreamState, 0, n)
	for i := uint64(0); i < n; i++ {
		var s StreamState
		s.StreamID = p.varint()
		s.RecvOffset = p.varint()
		s.ReadOffset = p.varint()
		s.Flags = p.varint()
		s.Target = p.stringField()
		if p.err != nil {
			return nil
		}
		states = append(states, s)
	}
	return states
}

func (p *parser) done() error {
	if p.err != nil {
		return p.err
	}
	if len(p.b) != 0 {
		return fmt.Errorf("%w: %d trailing bytes", ErrMalformed, len(p.b))
	}
	return nil
}

// ParseFrame decodes a frame body (type + payload, without the length prefix).
func ParseFrame(body []byte) (Frame, error) {
	p := &parser{b: body}
	typ := FrameType(p.varint())
	if p.err != nil {
		return nil, p.err
	}
	var f Frame
	switch typ {
	case TypeHello:
		h := &Hello{}
		h.Version = p.varint()
		if id := p.raw(len(h.SessionID)); id != nil {
			copy(h.SessionID[:], id)
		}
		h.Epoch = p.varint()
		h.Flags = p.varint()
		h.Window = p.varint()
		h.Token = p.bytesField()
		h.States = p.states()
		if h.Flags&FlagKeyAuth != 0 {
			h.Ticket = p.bytesField()
			h.Proofs = p.proofs()
		}
		f = h
	case TypeHelloAck:
		h := &HelloAck{}
		h.Code = p.varint()
		h.Reason = p.stringField()
		h.Window = p.varint()
		h.States = p.states()
		if p.err == nil && len(p.b) > 0 {
			h.Ticket = p.bytesField()
		}
		f = h
	case TypeOpen:
		o := &Open{}
		o.StreamID = p.varint()
		o.Target = p.stringField()
		f = o
	case TypeData:
		d := &Data{}
		d.StreamID = p.varint()
		d.Offset = p.varint()
		d.Payload = p.bytesField()
		f = d
	case TypeAck:
		a := &Ack{}
		a.StreamID = p.varint()
		a.RecvOffset = p.varint()
		a.ReadOffset = p.varint()
		f = a
	case TypeFin:
		fin := &Fin{}
		fin.StreamID = p.varint()
		fin.FinalOffset = p.varint()
		f = fin
	case TypeReset:
		r := &Reset{}
		r.StreamID = p.varint()
		r.Code = p.varint()
		f = r
	case TypePing:
		f = &Ping{Nonce: p.varint()}
	case TypePong:
		f = &Pong{Nonce: p.varint()}
	case TypeClose:
		c := &Close{}
		c.Code = p.varint()
		c.Reason = p.stringField()
		f = c
	default:
		return nil, fmt.Errorf("%w: unknown frame type 0x%x", ErrMalformed, uint64(typ))
	}
	if err := p.done(); err != nil {
		return nil, err
	}
	return f, nil
}
