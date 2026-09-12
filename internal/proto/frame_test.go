package proto

import (
	"bytes"
	"io"
	"net"
	"reflect"
	"testing"
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
		return &c
	case *HelloAck:
		c := *v
		if len(c.States) == 0 {
			c.States = nil
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
