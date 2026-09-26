package mux

import (
	"errors"
	"fmt"
	"time"

	"github.com/0x0079/tingly-ssh/internal/proto"
)

// maxBatchBytes caps how much stream data one writer pass emits before it
// yields, so a bulk transfer cannot starve interactive streams.
const maxBatchBytes = 256 << 10

// errLinkSuperseded ends a writer whose link is no longer current.
var errLinkSuperseded = errors.New("mux: link superseded")

// readLoop decodes frames until the link fails. A link failure is returned as
// is; a peer protocol violation terminates the whole session.
func (s *Session) readLoop(ls *linkState) error {
	for {
		f, err := ls.conn.ReadFrame()
		if err != nil {
			if errors.Is(err, proto.ErrMalformed) {
				perr := &ProtocolError{err: err}
				s.terminate(perr)
				return perr
			}
			return err
		}
		if err := s.handleFrame(f); err != nil {
			return err
		}
	}
}

func (s *Session) handleFrame(f proto.Frame) error {
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}

	fail := func(err error) error {
		s.mu.Unlock()
		perr := &ProtocolError{err: err}
		s.terminate(perr)
		return perr
	}

	switch v := f.(type) {
	case *proto.Open:
		if s.role != RoleServer {
			return fail(fmt.Errorf("client received OPEN for stream %d", v.StreamID))
		}
		if _, exists := s.streams[v.StreamID]; exists {
			break // replayed OPEN; the stream is already live
		}
		if len(s.streams) >= s.cfg.MaxStreams {
			s.enqueueLocked(&proto.Reset{StreamID: v.StreamID, Code: proto.CodeInternal})
			break
		}
		st := &Stream{sess: s, id: v.StreamID, target: v.Target, sendLimit: s.peerWindow}
		s.addStreamLocked(st)
		select {
		case s.incoming <- st:
		default:
			s.removeStreamLocked(v.StreamID)
			s.enqueueLocked(&proto.Reset{StreamID: v.StreamID, Code: proto.CodeInternal})
		}

	case *proto.Data:
		st, ok := s.streams[v.StreamID]
		if !ok {
			break // stream already gone; the peer will see our RESET
		}
		if err := st.acceptDataLocked(v.Offset, v.Payload, s.window); err != nil {
			return fail(err)
		}
		s.notifyLocked() // ACK the new bytes

	case *proto.Ack:
		st, ok := s.streams[v.StreamID]
		if !ok {
			break
		}
		if err := st.send.release(v.RecvOffset); err != nil {
			return fail(fmt.Errorf("stream %d: %w", v.StreamID, err))
		}
		if limit := v.ReadOffset + s.peerWindow; limit > st.sendLimit {
			st.sendLimit = limit
		}
		s.cond.Broadcast() // blocked writers may proceed

	case *proto.Fin:
		st, ok := s.streams[v.StreamID]
		if !ok {
			break
		}
		if st.finRecv && st.finalOff != v.FinalOffset {
			return fail(fmt.Errorf("stream %d: FIN offset changed %d -> %d", v.StreamID, st.finalOff, v.FinalOffset))
		}
		if st.recvOffset() > v.FinalOffset {
			return fail(fmt.Errorf("stream %d: FIN at %d below received %d", v.StreamID, v.FinalOffset, st.recvOffset()))
		}
		st.finRecv = true
		st.finalOff = v.FinalOffset
		s.cond.Broadcast()

	case *proto.Reset:
		if st, ok := s.streams[v.StreamID]; ok {
			st.failLocked(&StreamResetError{Code: v.Code})
			s.removeStreamLocked(v.StreamID)
		}

	case *proto.Ping:
		s.enqueueLocked(&proto.Pong{Nonce: v.Nonce})

	case *proto.Pong:
		// Liveness is inferred from link activity; nothing to do.

	case *proto.Close:
		s.mu.Unlock()
		err := &PeerCloseError{Code: v.Code, Reason: v.Reason}
		s.terminate(err)
		return err

	default:
		return fail(fmt.Errorf("unexpected %s frame on an established link", f.Type()))
	}

	s.mu.Unlock()
	return nil
}

// writeLoop serves one link until it is superseded, dies, or the session ends.
func (s *Session) writeLoop(ls *linkState) error {
	ticker := time.NewTicker(s.cfg.KeepAlive)
	defer ticker.Stop()
	var nonce uint64
	for {
		frames, more, closing, err := s.collect(ls.gen)
		if err != nil {
			return err
		}
		if len(frames) > 0 {
			for _, f := range frames {
				if err := ls.conn.WriteFrame(f); err != nil {
					return err
				}
			}
			if err := ls.conn.Flush(); err != nil {
				return err
			}
		}
		if closing {
			s.terminate(fmt.Errorf("%w: local close", ErrSessionClosed))
			return nil
		}
		if more {
			continue
		}
		select {
		case <-s.wake:
		case <-ticker.C:
			nonce++
			s.mu.Lock()
			s.enqueueLocked(&proto.Ping{Nonce: nonce})
			s.mu.Unlock()
		case <-ls.dead:
			return nil
		case <-s.done:
			return nil
		}
	}
}

// collect builds the next batch of frames for the link generation gen. It
// reports whether more stream data is queued (more) and whether a CLOSE was
// just emitted (closing).
func (s *Session) collect(gen uint64) (frames []proto.Frame, more, closing bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, false, ErrSessionClosed
	}
	if s.linkGen != gen {
		return nil, false, false, errLinkSuperseded
	}

	if len(s.ctrl) > 0 {
		frames = append(frames, s.ctrl...)
		for _, f := range s.ctrl {
			if _, ok := f.(*proto.Close); ok {
				closing = true
			}
		}
		s.ctrl = s.ctrl[:0]
	}

	budget := maxBatchBytes
	var retired []uint64
	n := len(s.order)
	for i := 0; i < n; i++ {
		idx := (s.rr + i) % n
		st := s.streams[s.order[idx]]
		if st == nil {
			continue
		}
		if st.needsOpen {
			frames = append(frames, &proto.Open{StreamID: st.id, Target: st.target})
			st.needsOpen = false
		}
		if recv := st.recvOffset(); recv != st.ackedRecv || st.readOffset != st.ackedRead {
			frames = append(frames, &proto.Ack{StreamID: st.id, RecvOffset: recv, ReadOffset: st.readOffset})
			st.ackedRecv, st.ackedRead = recv, st.readOffset
		}
		for budget > 0 {
			chunk := st.send.pending(min(proto.MaxDataPayload, budget))
			if len(chunk) == 0 {
				break
			}
			payload := make([]byte, len(chunk))
			copy(payload, chunk)
			frames = append(frames, &proto.Data{StreamID: st.id, Offset: st.send.cursor, Payload: payload})
			st.send.advance(uint64(len(payload)))
			budget -= len(payload)
		}
		if st.send.cursor < st.send.end() {
			more = true
		} else {
			if st.finQueued {
				frames = append(frames, &proto.Fin{StreamID: st.id, FinalOffset: st.send.end()})
				st.finQueued = false
				st.finSent = true
			}
			if st.retire {
				retired = append(retired, st.id)
			}
		}
	}
	if n > 0 {
		s.rr = (s.rr + 1) % n
	}
	// Removal is deferred to here so s.order stays stable during the pass.
	for _, id := range retired {
		s.removeStreamLocked(id)
	}
	return frames, more, closing, nil
}
