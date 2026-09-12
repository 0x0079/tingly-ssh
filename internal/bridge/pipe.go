// Package bridge turns the session layer into the two halves of the tunnel:
// a client that accepts local TCP (or stdio) and a server that dials sshd.
package bridge

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
)

// halfCloser is a byte stream that can signal "no more data from me" without
// tearing down the other direction. Both *net.TCPConn and *mux.Stream do.
type halfCloser interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// stdio adapts the process's standard input/output to halfCloser for
// ProxyCommand mode.
type stdio struct {
	in  io.ReadCloser
	out io.WriteCloser
}

// Stdio returns a halfCloser over os.Stdin and os.Stdout.
func Stdio() halfCloser { return &stdio{in: os.Stdin, out: os.Stdout} }

func (s *stdio) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s *stdio) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s *stdio) CloseWrite() error           { return s.out.Close() }
func (s *stdio) Close() error {
	err := s.out.Close()
	if err2 := s.in.Close(); err == nil {
		err = err2
	}
	return err
}

// Pipe copies bytes both ways until both directions finish, propagating each
// EOF as a half close. Half-close propagation is what lets scp and `ssh -W`
// terminate instead of hanging.
func Pipe(a, b halfCloser) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = copyHalf(a, b) }()
	go func() { defer wg.Done(); errs[1] = copyHalf(b, a) }()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	return errors.Join(errs...)
}

func copyHalf(dst, src halfCloser) error {
	_, err := io.Copy(dst, src)
	if cerr := dst.CloseWrite(); err == nil && !isExpected(cerr) {
		err = cerr
	}
	if isExpected(err) {
		return nil
	}
	return err
}

// isExpected reports errors that are the normal end of a connection.
func isExpected(err error) bool {
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed),
		errors.Is(err, os.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return true
	}
	return false
}
