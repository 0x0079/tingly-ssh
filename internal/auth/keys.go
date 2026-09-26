package auth

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/0x0079/tingly-ssh/internal/proto"
)

// KeyIDPrefix marks a session identity that came from an SSH key. The rest is
// the key's SHA-256 fingerprint, so two keys that share a comment are still
// two identities.
const KeyIDPrefix = "ssh:"

// ErrBadSignature means an authorized key was offered but its signature did
// not verify against this connection.
var ErrBadSignature = errors.New("auth: key signature does not match this connection")

// KeyEntry is one line of an authorized keys file.
type KeyEntry struct {
	Key      ssh.PublicKey
	Comment  string
	NotAfter time.Time // zero means it never expires
	// CA marks a cert-authority line: the key signs user certificates rather
	// than being offered itself.
	CA bool
	// Principals restricts which certificates a CA line accepts; empty means
	// any principal.
	Principals []string
}

// KeyIdentity is who a verified key proof belongs to.
type KeyIdentity struct {
	ID    string        // what sessions bind to: "ssh:" + fingerprint
	Label string        // what logs show: the comment, or the certificate key id
	Key   ssh.PublicKey // the key as presented, certificate included
}

// KeyStore is a reloadable authorized keys file. Its format is OpenSSH's
// authorized_keys, so an operator can copy a user's line verbatim
// (.design/ssh-key-auth.pencil.md §5).
type KeyStore struct {
	path string

	mu      sync.RWMutex
	entries []KeyEntry
}

// LoadKeyStore reads an authorized keys file.
func LoadKeyStore(path string) (*KeyStore, error) {
	s := &KeyStore{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewKeyStore builds an in-memory store, for tests and callers that already
// hold the file contents.
func NewKeyStore(raw []byte) (*KeyStore, error) {
	entries, err := ParseAuthorizedKeys(raw)
	if err != nil {
		return nil, err
	}
	return &KeyStore{entries: entries}, nil
}

// Reload re-reads the file. On error the previous set stays in place, so a
// typo cannot lock everyone out.
func (s *KeyStore) Reload() error {
	if s.path == "" {
		return errors.New("auth: key store has no file to reload")
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	entries, err := ParseAuthorizedKeys(raw)
	if err != nil {
		return fmt.Errorf("auth: %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

// Replace swaps in a new set of entries, for tests that revoke keys without a
// file.
func (s *KeyStore) Replace(raw []byte) error {
	entries, err := ParseAuthorizedKeys(raw)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

// Path returns the file the store was loaded from.
func (s *KeyStore) Path() string { return s.path }

// Len reports how many lines are loaded.
func (s *KeyStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Labels lists the loaded entries for startup logging.
func (s *KeyStore) Labels() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		label := entryLabel(e)
		if e.CA {
			label = "ca:" + label
		}
		out = append(out, label)
	}
	return out
}

func entryLabel(e KeyEntry) string {
	if e.Comment != "" {
		return e.Comment
	}
	return ssh.FingerprintSHA256(e.Key)
}

// ignoredOptions are authorized_keys options that describe the SSH session,
// not access to the tunnel. sshd enforces them on the session itself, so the
// tunnel accepts and ignores them; that is what lets a line be copied as is.
var ignoredOptions = map[string]bool{
	"agent-forwarding": true, "command": true, "environment": true,
	"no-agent-forwarding": true, "no-port-forwarding": true, "no-pty": true,
	"no-user-rc": true, "no-x11-forwarding": true, "permitlisten": true,
	"permitopen": true, "port-forwarding": true, "pty": true, "restrict": true,
	"tunnel": true, "user-rc": true, "x11-forwarding": true,
	// FIDO options: the tunnel always requires the user-presence flag, which
	// is the stricter reading of no-touch-required. verify-required is left to
	// sshd, which still checks it when the user logs in.
	"no-touch-required": true, "verify-required": true,
}

// ParseAuthorizedKeys parses OpenSSH authorized_keys content. Options that
// would restrict access in a way the tunnel cannot honour are errors rather
// than silently ignored: an operator must never believe a line is narrower
// than it is.
func ParseAuthorizedKeys(raw []byte) ([]KeyEntry, error) {
	var entries []KeyEntry
	for lineNo, line := range bytes.Split(raw, []byte("\n")) {
		text := bytes.TrimSpace(line)
		if len(text) == 0 || text[0] == '#' {
			continue
		}
		pub, comment, options, _, err := ssh.ParseAuthorizedKey(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		e := KeyEntry{Key: pub, Comment: comment}
		if err := applyOptions(&e, options); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		if e.Key.Type() == "ssh-dss" {
			return nil, fmt.Errorf("line %d: DSA keys are not accepted", lineNo+1)
		}
		if _, isCert := e.Key.(*ssh.Certificate); isCert {
			return nil, fmt.Errorf("line %d: list the certificate's CA with cert-authority, not the certificate", lineNo+1)
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, errors.New("no keys found")
	}
	return entries, nil
}

func applyOptions(e *KeyEntry, options []string) error {
	for _, opt := range options {
		name, value, hasValue := strings.Cut(opt, "=")
		name = strings.ToLower(name)
		value = strings.Trim(value, `"`)
		switch {
		case name == "cert-authority":
			e.CA = true
		case name == "principals" && hasValue:
			for _, p := range strings.Split(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					e.Principals = append(e.Principals, p)
				}
			}
		case name == "expiry-time" && hasValue:
			t, err := ParseExpiryTime(value)
			if err != nil {
				return err
			}
			e.NotAfter = t
		case name == "from":
			// sshd behind the tunnel sees the tunnel's address, not the
			// user's, so this line would silently stop meaning anything.
			return errors.New(`option "from" is not supported: the tunnel cannot vouch for it and sshd no longer sees the client address; drop it or use a certificate with source-address`)
		case ignoredOptions[name]:
		default:
			return fmt.Errorf("unknown option %q", name)
		}
	}
	if len(e.Principals) > 0 && !e.CA {
		return errors.New(`option "principals" only applies to cert-authority lines`)
	}
	return nil
}

// ParseExpiryTime reads OpenSSH's expiry-time: YYYYMMDD or YYYYMMDDHHMM[SS],
// in local time unless suffixed with Z. A bare date means the start of that
// day, as in OpenSSH.
func ParseExpiryTime(s string) (time.Time, error) {
	loc := time.Local
	if rest, ok := strings.CutSuffix(s, "Z"); ok {
		s, loc = rest, time.UTC
	}
	for _, layout := range []string{"20060102", "200601021504", "20060102150405"} {
		if len(s) == len(layout) {
			if t, err := time.ParseInLocation(layout, s, loc); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("expiry-time %q is not YYYYMMDD[HHMM[SS]][Z]", s)
}

// Authorize decides whether a presented key may open a link, without looking
// at any signature. Resume tickets use it on its own, so revoking a key also
// stops the sessions it opened from reconnecting. remote is the client's
// address, used for a certificate's source-address option.
func (s *KeyStore) Authorize(pub ssh.PublicKey, remote net.IP, now time.Time) (KeyIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cert, ok := pub.(*ssh.Certificate); ok {
		return s.authorizeCertLocked(cert, remote, now)
	}
	wire := pub.Marshal()
	for _, e := range s.entries {
		if e.CA || !bytes.Equal(e.Key.Marshal(), wire) {
			continue
		}
		id := KeyIdentity{ID: KeyIDPrefix + ssh.FingerprintSHA256(pub), Label: entryLabel(e), Key: pub}
		if !e.NotAfter.IsZero() && !now.Before(e.NotAfter) {
			return id, fmt.Errorf("%w: %s (expiry-time %s)", ErrExpired, id.Label, e.NotAfter.UTC().Format(time.RFC3339))
		}
		return id, nil
	}
	return KeyIdentity{}, ErrUnauthorized
}

func (s *KeyStore) authorizeCertLocked(cert *ssh.Certificate, remote net.IP, now time.Time) (KeyIdentity, error) {
	if cert.CertType != ssh.UserCert {
		return KeyIdentity{}, fmt.Errorf("%w: not a user certificate", ErrUnauthorized)
	}
	signer := cert.SignatureKey.Marshal()
	for _, e := range s.entries {
		if !e.CA || !bytes.Equal(e.Key.Marshal(), signer) {
			continue
		}
		id := KeyIdentity{ID: KeyIDPrefix + ssh.FingerprintSHA256(cert.Key), Label: "cert:" + cert.KeyId, Key: cert}
		if !e.NotAfter.IsZero() && !now.Before(e.NotAfter) {
			return id, fmt.Errorf("%w: certificate authority %s (expiry-time %s)", ErrExpired, entryLabel(e), e.NotAfter.UTC().Format(time.RFC3339))
		}
		checker := &ssh.CertChecker{
			Clock: func() time.Time { return now },
			// sshd enforces force-command on the session; source-address is
			// checked below, because CheckCert leaves it to its caller.
			SupportedCriticalOptions: []string{"force-command", "source-address"},
		}
		if err := checker.CheckCert(pickPrincipal(e.Principals, cert.ValidPrincipals), cert); err != nil {
			return id, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		if len(e.Principals) > 0 && !intersects(e.Principals, cert.ValidPrincipals) {
			return id, fmt.Errorf("%w: certificate principals %q not allowed by %s", ErrUnauthorized, cert.ValidPrincipals, entryLabel(e))
		}
		if allowed, ok := cert.CriticalOptions["source-address"]; ok {
			if err := checkSourceAddress(allowed, remote); err != nil {
				return id, fmt.Errorf("%w: %v", ErrUnauthorized, err)
			}
		}
		return id, nil
	}
	return KeyIdentity{}, fmt.Errorf("%w: certificate signed by an unknown authority", ErrUnauthorized)
}

// pickPrincipal chooses the principal CheckCert validates against. The tunnel
// never learns the SSH user name, so it accepts a certificate that is valid
// for any principal the CA line allows (or any at all without principals=).
func pickPrincipal(allowed, valid []string) string {
	for _, p := range valid {
		if len(allowed) == 0 || contains(allowed, p) {
			return p
		}
	}
	return ""
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func checkSourceAddress(allowed string, remote net.IP) error {
	if remote == nil {
		return errors.New("certificate requires source-address but the client address is unknown")
	}
	for _, part := range strings.Split(allowed, ",") {
		part = strings.TrimSpace(part)
		if _, network, err := net.ParseCIDR(part); err == nil {
			if network.Contains(remote) {
				return nil
			}
			continue
		}
		if ip := net.ParseIP(part); ip != nil && ip.Equal(remote) {
			return nil
		}
	}
	return fmt.Errorf("client address %s is not in the certificate's source-address %q", remote, allowed)
}

// Verify finds the first proof whose key is authorized and whose signature
// covers message. Every proof is examined so the reported failure is the
// most useful one: an expired key beats a bad signature beats an unknown key.
func (s *KeyStore) Verify(proofs []proto.KeyProof, message []byte, remote net.IP, now time.Time) (KeyIdentity, error) {
	if len(proofs) == 0 {
		return KeyIdentity{}, fmt.Errorf("%w: no key offered", ErrUnauthorized)
	}
	var best error
	rank := func(err error) int {
		switch {
		case errors.Is(err, ErrExpired):
			return 3
		case errors.Is(err, ErrBadSignature):
			return 2
		default:
			return 1
		}
	}
	for _, p := range proofs {
		pub, err := ssh.ParsePublicKey(p.PublicKey)
		if err != nil {
			continue
		}
		var id KeyIdentity
		id, err = s.Authorize(pub, remote, now)
		if err == nil {
			if err = VerifySignature(pub, p.Signature, message); err == nil {
				return id, nil
			}
			err = fmt.Errorf("%w: %s: %v", ErrBadSignature, id.Label, err)
		}
		if best == nil || rank(err) > rank(best) {
			best = err
		}
	}
	if best == nil {
		best = fmt.Errorf("%w: no offered key could be parsed", ErrUnauthorized)
	}
	return KeyIdentity{}, best
}
