// Package auth turns a credentials file into client identities.
//
// A credential is a per-device token. The server stores only the token's
// SHA-256, so compromising the server yields nothing reusable, and each entry
// carries a label and an optional expiry. The label is the identity the rest
// of the system binds sessions and quotas to, and logs attribute work to
// (docs/adr/0004-client-identity.md §8.1).
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// HashPrefix marks a hashed token in a credentials file.
const HashPrefix = "sha256:"

// SharedLabel is the identity used when the file holds one raw shared token,
// which is the pre-identity format we still accept.
const SharedLabel = "shared"

var (
	// ErrUnauthorized means no credential matched.
	ErrUnauthorized = errors.New("auth: unknown credential")
	// ErrExpired means a credential matched but its not-after has passed.
	ErrExpired = errors.New("auth: credential expired")
)

// Credential is one authorized client.
type Credential struct {
	Label    string
	Hash     [32]byte
	NotAfter time.Time // zero means it never expires
}

// Expired reports whether the credential is past its not-after.
func (c Credential) Expired(now time.Time) bool {
	return !c.NotAfter.IsZero() && now.After(c.NotAfter)
}

// Hash returns the stored form of a token.
func Hash(token []byte) [32]byte { return sha256.Sum256(token) }

// Encode renders a hash the way a credentials file spells it.
func Encode(h [32]byte) string {
	return HashPrefix + base64.StdEncoding.EncodeToString(h[:])
}

// Record renders the line an operator pastes into the credentials file.
func Record(label string, token []byte, notAfter time.Time) string {
	line := fmt.Sprintf("%-24s %s", label, Encode(Hash(token)))
	if !notAfter.IsZero() {
		line += "  " + notAfter.UTC().Format("2006-01-02")
	}
	return line
}

// Store is a reloadable set of credentials.
type Store struct {
	path string

	mu     sync.RWMutex
	creds  []Credential
	shared bool // the file holds one raw token rather than hashed records
}

// LoadStore reads a credentials file.
//
// Two formats are accepted. The modern one is one record per line:
//
//	laptop-mbp14   sha256:<base64>   2027-06-01
//
// The other is a single raw token, which is what deployments had before
// identities existed; it keeps working and reports the identity "shared".
// A raw token is a secret, so that form must be mode 0600; a file of hashes
// and labels is not, so it is not required to be.
func LoadStore(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Single builds an in-memory store holding one shared token. It exists for
// callers that already have the secret in hand, such as tests.
func Single(token []byte) *Store {
	return &Store{creds: []Credential{{Label: SharedLabel, Hash: Hash(token)}}, shared: true}
}

// Reload re-reads the file. On any error the previous contents stay in place,
// so a typo during an edit cannot lock every client out.
func (s *Store) Reload() error {
	if s.path == "" {
		return errors.New("auth: store has no file to reload")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	creds, shared, err := parse(raw)
	if err != nil {
		return fmt.Errorf("auth: %s: %w", s.path, err)
	}
	if shared {
		// The file contains the secret itself, so its permissions matter.
		info, err := os.Stat(s.path)
		if err != nil {
			return fmt.Errorf("auth: stat %s: %w", s.path, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return fmt.Errorf("auth: %s holds a raw token but has mode %04o, want 0600", s.path, mode)
		}
	}
	s.mu.Lock()
	s.creds, s.shared = creds, shared
	s.mu.Unlock()
	return nil
}

func parse(raw []byte) ([]Credential, bool, error) {
	var lines [][]string
	for _, line := range strings.Split(string(raw), "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		lines = append(lines, strings.Fields(text))
	}
	if len(lines) == 0 {
		return nil, false, errors.New("no credentials found")
	}
	// A single bare word is the pre-identity format: the token itself. It only
	// means that when it is the whole file, so a stray word next to real
	// records is an error rather than a silently ignored line.
	if len(lines) == 1 && len(lines[0]) == 1 && !strings.HasPrefix(lines[0][0], HashPrefix) {
		return []Credential{{Label: SharedLabel, Hash: Hash([]byte(lines[0][0]))}}, true, nil
	}
	creds := make([]Credential, 0, len(lines))
	for i, fields := range lines {
		c, err := parseRecord(fields)
		if err != nil {
			return nil, false, fmt.Errorf("record %d: %w", i+1, err)
		}
		creds = append(creds, c)
	}
	return creds, false, nil
}

func parseRecord(fields []string) (Credential, error) {
	var c Credential
	if len(fields) < 2 {
		return c, fmt.Errorf("want 'label sha256:<base64> [not-after]', got %q", strings.Join(fields, " "))
	}
	c.Label = fields[0]
	digest, ok := strings.CutPrefix(fields[1], HashPrefix)
	if !ok {
		return c, fmt.Errorf("hash must start with %q", HashPrefix)
	}
	sum, err := base64.StdEncoding.DecodeString(digest)
	if err != nil {
		return c, fmt.Errorf("decode hash: %w", err)
	}
	if len(sum) != sha256.Size {
		return c, fmt.Errorf("hash is %d bytes, want %d", len(sum), sha256.Size)
	}
	copy(c.Hash[:], sum)
	if len(fields) > 2 {
		c.NotAfter, err = ParseNotAfter(fields[2])
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// ParseNotAfter accepts a date or an RFC 3339 timestamp. A bare date means
// "valid through that day", so it expires at the end of it in UTC.
func ParseNotAfter(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	day, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("not-after %q is neither a date nor an RFC 3339 timestamp", s)
	}
	return day.Add(24*time.Hour - time.Second), nil
}

// Lookup identifies the credential a token belongs to. Every candidate is
// compared, in constant time, so a failure leaks nothing about which entries
// exist.
func (s *Store) Lookup(token []byte) (Credential, error) {
	want := Hash(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var match *Credential
	for i := range s.creds {
		if subtle.ConstantTimeCompare(s.creds[i].Hash[:], want[:]) == 1 {
			match = &s.creds[i]
		}
	}
	if match == nil {
		return Credential{}, ErrUnauthorized
	}
	if match.Expired(time.Now()) {
		return *match, fmt.Errorf("%w: %s (not-after %s)", ErrExpired, match.Label,
			match.NotAfter.UTC().Format(time.RFC3339))
	}
	return *match, nil
}

// Path returns the file the store was loaded from, or "" for an in-memory one.
func (s *Store) Path() string { return s.path }

// Len reports how many credentials are loaded.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.creds)
}

// Shared reports whether the store is a single pre-identity token.
func (s *Store) Shared() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shared
}

// Labels lists the loaded identities, for startup logging.
func (s *Store) Labels() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.creds))
	for _, c := range s.creds {
		out = append(out, c.Label)
	}
	return out
}
