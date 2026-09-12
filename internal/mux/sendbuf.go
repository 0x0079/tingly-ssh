package mux

import "fmt"

// sendBuffer is a stream's replay buffer. It holds the bytes in
// [base, base+len(data)) and remembers how far they have been transmitted on
// the current link.
//
//	base    : everything below has been acknowledged by the peer and dropped
//	cursor  : next byte to put on the wire (rewound on resume)
//	end     : next byte the application will append
//
// Invariant: base <= cursor <= end.
type sendBuffer struct {
	data   []byte
	base   uint64
	cursor uint64
}

func (s *sendBuffer) end() uint64 { return s.base + uint64(len(s.data)) }

// buffered reports how many bytes are retained for replay.
func (s *sendBuffer) buffered() int { return len(s.data) }

func (s *sendBuffer) append(p []byte) {
	s.data = append(s.data, p...)
}

// release drops bytes the peer has confirmed receiving. Offsets that move
// backwards are ignored (duplicate or reordered ACKs are harmless).
func (s *sendBuffer) release(upTo uint64) error {
	if upTo <= s.base {
		return nil
	}
	if upTo > s.end() {
		return fmt.Errorf("ack offset %d beyond sent data end %d", upTo, s.end())
	}
	n := upTo - s.base
	// Copy down rather than reslicing, so the backing array does not grow
	// without bound over the life of a long session.
	copy(s.data, s.data[n:])
	s.data = s.data[:uint64(len(s.data))-n]
	s.base = upTo
	if s.cursor < s.base {
		s.cursor = s.base
	}
	return nil
}

// rewind moves the transmit cursor to the offset the peer says it has
// received, which is what makes replay after a link failure exact.
func (s *sendBuffer) rewind(to uint64) error {
	if to < s.base {
		return fmt.Errorf("peer wants offset %d but replay buffer starts at %d", to, s.base)
	}
	if to > s.end() {
		return fmt.Errorf("peer claims offset %d but only %d bytes were produced", to, s.end())
	}
	if err := s.release(to); err != nil {
		return err
	}
	s.cursor = to
	return nil
}

// pending returns up to max untransmitted bytes starting at the cursor. The
// result aliases the buffer and is only valid until the next mutation, so
// callers copy it into the frame they emit.
func (s *sendBuffer) pending(max int) []byte {
	avail := s.end() - s.cursor
	if avail == 0 || max <= 0 {
		return nil
	}
	if uint64(max) < avail {
		avail = uint64(max)
	}
	off := s.cursor - s.base
	return s.data[off : off+avail]
}

func (s *sendBuffer) advance(n uint64) { s.cursor += n }
