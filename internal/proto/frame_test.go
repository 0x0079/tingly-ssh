package proto

import (
	"bytes"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/quic-go/quic-go/quicvarint"
)

func TestFrameRoundTrip(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	frames := []Frame{
		&Hello{Version: Version, SessionID: id, Epoch: 7, Flags: FlagResume, Window: DefaultWindow, Token: []byte("s3cret"), States: []StreamState{
			{StreamID: 1, RecvOffset: 1 << 20, ReadOffset: 1 << 19, Flags: FlagFinSent | FlagFinReceived, Target: "127.0.0.1:22"},
			{StreamID: 3},
		}},
		&Hello{Version: Version, SessionID: id},
		&HelloAck{Code: CodeOK, Reason: "", Window: 4096, States: []StreamState{{StreamID: 9, RecvOffset: 3}}},
		&HelloAck{Code: CodeUnauthorized, Reason: "bad token"},
		&Hello{Version: Version, SessionID: id, Flags: FlagKeyAuth | FlagResume, Ticket: []byte("ticket")},
		&Hello{Version: Version, SessionID: id, Epoch: 2, Flags: FlagKeyAuth, States: []StreamState{{StreamID: 1}}, Proofs: []KeyProof{
			{PublicKey: []byte("key-1"), Signature: []byte("sig-1")},
			{PublicKey: []byte("key-2"), Signature: []byte("sig-2")},
		}},
		&HelloAck{Code: CodeOK, Window: 4096, States: []StreamState{{StreamID: 1}}, Ticket: bytes.Repeat([]byte{7}, 32)},
		&Open{StreamID: 5, Target: "10.0.0.1:2222"},
		&Data{StreamID: 5, Offset: 0, Payload: []byte{}},
		&Data{StreamID: 5, Offset: 1 << 30, Payload: bytes.Repeat([]byte("x"), 1024)},
		&Ack{StreamID: 5, RecvOffset: 99, ReadOffset: 98},
		&Fin{StreamID: 5, FinalOffset: 12345},
		&Reset{StreamID: 5, Code: CodeProtocol},
		&Ping{Nonce: 42},
		&Pong{Nonce: 42},
		&Close{Code: CodeShutdown, Reason: "server going away"},
	}

	for _, f := range frames {
		enc, err := AppendFrame(nil, f)
		if err != nil {
			t.Fatalf("%s: encode: %v", f.Type(), err)
		}
		// Strip the length prefix the same way Conn.ReadFrame does.
		p := &parser{b: enc}
		n := p.varint()
		if err := p.done(); err == nil && false {
			t.Fatal("unreachable")
		}
		body := enc[len(enc)-int(n):]
		got, err := ParseFrame(body)
		if err != nil {
			t.Fatalf("%s: parse: %v", f.Type(), err)
		}
		if !reflect.DeepEqual(normalize(f), normalize(got)) {
			t.Fatalf("%s: round trip mismatch\n want %#v\n got  %#v", f.Type(), f, got)
		}
	}
}

// normalize makes empty/nil byte slices compare equal, which is irrelevant
// to the wire format.
func normalize(f Frame) Frame {
	switch v := f.(type) {
	case *Data:
		c := *v
		if len(c.Payload) == 0 {
			c.Payload = nil
		}
		return &c
	case *Hello:
		c := *v
		if len(c.Token) == 0 {
			c.Token = nil
		}
		if len(c.States) == 0 {
			c.States = nil
		}
		if len(c.Ticket) == 0 {
			c.Ticket = nil
		}
		if len(c.Proofs) == 0 {
			c.Proofs = nil
		}
		return &c
	case *HelloAck:
		c := *v
		if len(c.States) == 0 {
			c.States = nil
		}
		if len(c.Ticket) == 0 {
			c.Ticket = nil
		}
		return &c
	}
	return f
}

func TestParseFrameRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"empty":             {},
		"unknown type":      {0x7f},
		"truncated data":    {byte(TypeData), 0x05, 0x00, 0x04, 'a'},
		"trailing bytes":    {byte(TypePing), 0x01, 0xff},
		"huge state count":  {byte(TypeHelloAck), 0x00, 0x00, 0x00, 0x7f},
		"truncated session": {byte(TypeHello), 0x01, 0x00},
	}
	for name, body := range cases {
		if f, err := ParseFrame(body); err == nil {
			t.Errorf("%s: expected error, got %#v", name, f)
		}
	}
}

// A HELLO without FlagKeyAuth must stay byte-for-byte what older servers
// parse, and a HELLO_ACK without a ticket what older clients parse: the key
// fields exist on the wire only when they are in use.
func TestKeyAuthFieldsAreInvisibleWhenUnused(t *testing.T) {
	id := SessionID{1, 2, 3}
	plain := &Hello{Version: Version, SessionID: id, Epoch: 3, Window: 10, Token: []byte("t"), States: []StreamState{{StreamID: 1}}}
	withJunk := *plain
	withJunk.Ticket = []byte("ignored without the flag")
	withJunk.Proofs = []KeyProof{{PublicKey: []byte("k"), Signature: []byte("s")}}
	a, _ := AppendFrame(nil, plain)
	b, _ := AppendFrame(nil, &withJunk)
	if !bytes.Equal(a, b) {
		t.Fatal("key fields leaked onto the wire of a HELLO without FlagKeyAuth")
	}
	// Legacy encoding, spelled out: version session epoch flags window token states.
	legacy := []byte{byte(TypeHello), 1}
	legacy = append(legacy, id[:]...)
	legacy = append(legacy, 3, 0, 10, 1, 't', 1, 1, 0, 0, 0, 0)
	if !bytes.Equal(a[1:], legacy) {
		t.Fatalf("HELLO wire format changed:\n got  %x\n want %x", a[1:], legacy)
	}

	ack, _ := AppendFrame(nil, &HelloAck{Code: CodeOK, Window: 10})
	if !bytes.Equal(ack[1:], []byte{byte(TypeHelloAck), 0, 0, 10, 0}) {
		t.Fatalf("HELLO_ACK wire format changed: %x", ack[1:])
	}
}

func TestHelloRejectsTooManyProofs(t *testing.T) {
	h := &Hello{Version: Version, Flags: FlagKeyAuth}
	for range MaxKeyProofs + 1 {
		h.Proofs = append(h.Proofs, KeyProof{PublicKey: []byte("k"), Signature: []byte("s")})
	}
	enc, err := AppendFrame(nil, h)
	if err != nil {
		t.Fatal(err)
	}
	_, n, _ := quicvarint.Parse(enc)
	if _, err := ParseFrame(enc[n:]); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "key proofs") {
		t.Fatalf("a HELLO with too many key proofs: err = %v", err)
	}
}

func TestConnRoundTripOverPipe(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca, cb := NewConn(a), NewConn(b)

	want := []Frame{
		&Open{StreamID: 1, Target: "127.0.0.1:22"},
		&Data{StreamID: 1, Offset: 0, Payload: []byte("hello")},
		&Data{StreamID: 1, Offset: 5, Payload: bytes.Repeat([]byte{0xab}, MaxDataPayload)},
		&Fin{StreamID: 1, FinalOffset: 5 + MaxDataPayload},
	}
	done := make(chan error, 1)
	go func() {
		for _, f := range want {
			if err := ca.WriteFrame(f); err != nil {
				done <- err
				return
			}
		}
		done <- ca.Flush()
	}()

	for _, exp := range want {
		got, err := cb.ReadFrame()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !reflect.DeepEqual(normalize(exp), normalize(got)) {
			t.Fatalf("mismatch: want %#v got %#v", exp, got)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestConnRejectsOversizedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		// 4-byte varint (0b10 prefix) encoding 0x3fffffff, far above MaxFrameSize.
		a.Write([]byte{0xbf, 0xff, 0xff, 0xff})
	}()
	if _, err := NewConn(b).ReadFrame(); err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestConnReadFrameEOF(t *testing.T) {
	a, b := net.Pipe()
	a.Close()
	defer b.Close()
	if _, err := NewConn(b).ReadFrame(); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}
