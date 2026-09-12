package mux

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// ErrStreamClosed is returned by Write after the local side closed its
// writing half.
var ErrStreamClosed = errors.New("mux: stream write half closed")

// errLocalClose terminates pending Read/Write calls after a local Close.
var errLocalClose = errors.New("mux: stream closed locally")

// StreamResetError reports that the peer aborted the stream.
type StreamResetError struct{ Code uint64 }

func (e *StreamResetError) Error() string {
	return fmt.Sprintf("mux: stream reset by peer (%s)", proto.CodeName(e.Code))
}

// Stream is a logical, resumable, ordered byte stream inside a Session. It
// satisfies io.ReadWriteCloser and carries exactly one bridged TCP connection.
//
// While no link is attached, Read and Write block instead of failing: that is
// invariant I5 in docs/03-session-resumption.md and the reason SSH does not
// notice a network change.
type Stream struct {
	sess   *Session
	id     uint64
	target string

	// All fields below are guarded by sess.mu; waiters use sess.cond.
	send       sendBuffer
	sendLimit  uint64 // peer readOffset + peer window
	writeEOF   bool   // application called CloseWrite
	finSent    bool   // FIN handed to the writer
	finQueued  bool   // FIN still to be transmitted on the current link
	needsOpen  bool   // OPEN still to be transmitted on the current link
	recvBuf    bytes.Buffer
	readOffset uint64
	finRecv    bool
	finalOff   uint64
	ackedRecv  uint64
	ackedRead  uint64
	err        error // terminal stream error (reset, session death)
}

// recvOffset is the total number of bytes accepted from the peer.
func (s *Stream) recvOffset() uint64 { return s.readOffset + uint64(s.recvBuf.Len()) }

// ID returns the stream's protocol identifier.
func (s *Stream) ID() uint64 { return s.id }

// Target returns the destination hint carried in OPEN.
func (s *Stream) Target() string { return s.target }

// Read implements io.Reader.
func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	for {
		if s.recvBuf.Len() > 0 {
			n, _ := s.recvBuf.Read(p)
			s.readOffset += uint64(n)
			// Credit the peer once a quarter window has been consumed; ACKs
			// are otherwise coalesced by the writer.
			if s.readOffset-s.ackedRead >= s.sess.window/4 {
				s.sess.notifyLocked()
			}
			return n, nil
		}
		if s.finRecv && s.recvOffset() >= s.finalOff {
			return 0, io.EOF
		}
		if s.err != nil {
			return 0, s.err
		}
		s.sess.cond.Wait()
	}
}

// Write implements io.Writer. It blocks while the peer's receive window is
// full, and while no link is attached.
func (s *Stream) Write(p []byte) (int, error) {
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	written := 0
	for written < len(p) {
		if s.err != nil {
			return written, s.err
		}
		if s.writeEOF {
			return written, ErrStreamClosed
		}
		room := int64(s.sendLimit) - int64(s.send.end())
		if room <= 0 {
			s.sess.cond.Wait()
			continue
		}
		n := len(p) - written
		if int64(n) > room {
			n = int(room)
		}
		s.send.append(p[written : written+n])
		written += n
		s.sess.notifyLocked()
	}
	return written, nil
}

// CloseWrite performs a half close: the peer will observe io.EOF after the
// bytes written so far. It mirrors shutdown(SHUT_WR) and is required for scp
// and `ssh -W` to terminate correctly.
func (s *Stream) CloseWrite() error {
	s.sess.mu.Lock()
	defer s.sess.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.writeEOF {
		return nil
	}
	s.writeEOF = true
	s.finQueued = true
	s.sess.notifyLocked()
	return nil
}

// Close tears the stream down. A stream that was closed gracefully in both
// directions is simply retired; anything else is reset so the peer does not
// wait forever.
func (s *Stream) Close() error {
	s.sess.mu.Lock()
	graceful := s.finSent && s.finRecv
	if !graceful && s.err == nil {
		s.sess.enqueueLocked(&proto.Reset{StreamID: s.id, Code: proto.CodeOK})
	}
	s.failLocked(errLocalClose)
	s.sess.removeStreamLocked(s.id)
	s.sess.mu.Unlock()
	return nil
}

// failLocked marks the stream terminally broken and wakes its waiters.
func (s *Stream) failLocked(err error) {
	if s.err == nil {
		s.err = err
	}
	s.sess.cond.Broadcast()
}

// stateLocked snapshots the stream for a HELLO/HELLO_ACK exchange.
func (s *Stream) stateLocked() proto.StreamState {
	var flags uint64
	if s.finRecv {
		flags |= proto.FlagFinReceived
	}
	if s.finSent {
		flags |= proto.FlagFinSent
	}
	return proto.StreamState{
		StreamID:   s.id,
		RecvOffset: s.recvOffset(),
		ReadOffset: s.readOffset,
		Flags:      flags,
		Target:     s.target,
	}
}

// acceptDataLocked applies a DATA frame, trimming the replayed prefix.
// Duplicate bytes are dropped; a gap or a window violation is a protocol error.
func (s *Stream) acceptDataLocked(offset uint64, payload []byte, window uint64) error {
	recv := s.recvOffset()
	if offset > recv {
		return fmt.Errorf("stream %d: gap at offset %d, expected %d", s.id, offset, recv)
	}
	end := offset + uint64(len(payload))
	if end <= recv {
		return nil // pure duplicate from a replay
	}
	if s.finRecv && end > s.finalOff {
		return fmt.Errorf("stream %d: %d bytes past FIN offset %d", s.id, end-s.finalOff, s.finalOff)
	}
	fresh := payload[recv-offset:]
	if uint64(s.recvBuf.Len())+uint64(len(fresh)) > window {
		return fmt.Errorf("stream %d: peer exceeded receive window (%d + %d > %d)",
			s.id, s.recvBuf.Len(), len(fresh), window)
	}
	s.recvBuf.Write(fresh)
	s.sess.cond.Broadcast()
	return nil
}
