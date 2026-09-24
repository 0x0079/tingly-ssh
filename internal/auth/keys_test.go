package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/0x0079/tingly-shell/internal/proto"
)

func newEd25519(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authorizedLine(s ssh.Signer, options, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	if options != "" {
		line = options + " " + line
	}
	if comment != "" {
		line += " " + comment
	}
	return line
}

func mustStore(t *testing.T, lines ...string) *KeyStore {
	t.Helper()
	ks, err := NewKeyStore([]byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// prove signs message with s the way a client does.
func prove(t *testing.T, s ssh.Signer, message []byte) proto.KeyProof {
	t.Helper()
	proofs, err := NewSignerProver(s).Prove(message)
	if err != nil {
		t.Fatal(err)
	}
	return proofs[0]
}

func TestParseAuthorizedKeys(t *testing.T) {
	alice, bob := newEd25519(t), newEd25519(t)
	raw := strings.Join([]string{
		"# comments and blank lines are skipped",
		"",
		authorizedLine(alice, "", "alice@laptop"),
		// Options that describe the SSH session are sshd's business, and are
		// accepted so a line can be copied verbatim.
		authorizedLine(bob, `no-pty,command="/bin/true",expiry-time="20300101Z"`, "bob"),
	}, "\n")
	entries, err := ParseAuthorizedKeys([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Comment != "alice@laptop" || entries[1].Comment != "bob" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC); !entries[1].NotAfter.Equal(want) {
		t.Fatalf("expiry-time = %v, want %v", entries[1].NotAfter, want)
	}
}

func TestParseAuthorizedKeysRejects(t *testing.T) {
	k := newEd25519(t)
	cases := map[string]string{
		"empty":                 "# nothing here\n",
		"garbage":               "ssh-ed25519 not-base64!!",
		"from option":           authorizedLine(k, `from="10.0.0.0/8"`, "x"),
		"unknown option":        authorizedLine(k, `frobnicate`, "x"),
		"bad expiry":            authorizedLine(k, `expiry-time="next tuesday"`, "x"),
		"principals without ca": authorizedLine(k, `principals="alice"`, "x"),
	}
	for name, raw := range cases {
		if _, err := ParseAuthorizedKeys([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseExpiryTime(t *testing.T) {
	for in, want := range map[string]time.Time{
		"20270102Z":       time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC),
		"202701021530Z":   time.Date(2027, 1, 2, 15, 30, 0, 0, time.UTC),
		"20270102153045Z": time.Date(2027, 1, 2, 15, 30, 45, 0, time.UTC),
		"20270102":        time.Date(2027, 1, 2, 0, 0, 0, 0, time.Local),
	} {
		got, err := ParseExpiryTime(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: got %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestVerifyAcceptsAuthorizedKey(t *testing.T) {
	alice, stranger := newEd25519(t), newEd25519(t)
	ks := mustStore(t, authorizedLine(alice, "", "alice@laptop"))
	msg := HelloMessage([]byte("exporter-A"), proto.SessionID{1})

	// The client offers every agent key; the first authorized one wins.
	id, err := ks.Verify([]proto.KeyProof{prove(t, stranger, msg), prove(t, alice, msg)}, msg, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if id.Label != "alice@laptop" || id.ID != KeyIDPrefix+ssh.FingerprintSHA256(alice.PublicKey()) {
		t.Fatalf("identity = %+v", id)
	}
}

func TestVerifyRefuses(t *testing.T) {
	alice, stranger := newEd25519(t), newEd25519(t)
	ks := mustStore(t,
		authorizedLine(alice, "", "alice"),
	)
	msg := HelloMessage([]byte("exporter-A"), proto.SessionID{1})
	now := time.Now()

	if _, err := ks.Verify([]proto.KeyProof{prove(t, stranger, msg)}, msg, nil, now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown key: err = %v", err)
	}
	if _, err := ks.Verify(nil, msg, nil, now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("no proofs: err = %v", err)
	}

	// The core property: a proof made on one connection is useless on
	// another, which is what a man in the middle would try.
	relayed := prove(t, alice, msg)
	other := HelloMessage([]byte("exporter-B"), proto.SessionID{1})
	if _, err := ks.Verify([]proto.KeyProof{relayed}, other, nil, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("relayed proof: err = %v", err)
	}
	// ...and one made for another session id is useless for this one.
	otherSession := HelloMessage([]byte("exporter-A"), proto.SessionID{2})
	if _, err := ks.Verify([]proto.KeyProof{relayed}, otherSession, nil, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("proof for another session: err = %v", err)
	}

	// A signature over the raw message, without the SSHSIG framing, must not
	// verify: accepting it would mean asking keys to sign raw data.
	raw, err := alice.Sign(rand.Reader, msg)
	if err != nil {
		t.Fatal(err)
	}
	bare := proto.KeyProof{PublicKey: alice.PublicKey().Marshal(), Signature: ssh.Marshal(raw)}
	if _, err := ks.Verify([]proto.KeyProof{bare}, msg, nil, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("unframed signature: err = %v", err)
	}

	if _, err := ks.Verify([]proto.KeyProof{{PublicKey: []byte("junk"), Signature: []byte("junk")}}, msg, nil, now); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("junk proof: err = %v", err)
	}
}

func TestVerifyReportsExpiry(t *testing.T) {
	alice := newEd25519(t)
	ks := mustStore(t, authorizedLine(alice, `expiry-time="20200101Z"`, "alice"))
	msg := HelloMessage([]byte("e"), proto.SessionID{})
	_, err := ks.Verify([]proto.KeyProof{prove(t, alice, msg)}, msg, nil, time.Now())
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want expiry", err)
	}
}

func TestSignatureNamespaceIsFixed(t *testing.T) {
	// The namespace is part of every signature ever issued; changing it is a
	// protocol change and must be a deliberate one.
	if SigNamespace != "tingly-shell-hello-v1" {
		t.Fatalf("namespace changed to %q", SigNamespace)
	}
	data := SignedData([]byte("m"))
	if !strings.HasPrefix(string(data), "SSHSIG") {
		t.Fatal("signed data does not start with the SSHSIG magic")
	}
}

func TestRSARequiresSHA2(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ks := mustStore(t, authorizedLine(signer, "", "rsa"))
	msg := HelloMessage([]byte("e"), proto.SessionID{})

	// The prover asks for rsa-sha2-512 on its own.
	good := prove(t, signer, msg)
	if _, err := ks.Verify([]proto.KeyProof{good}, msg, nil, time.Now()); err != nil {
		t.Fatalf("rsa-sha2-512: %v", err)
	}

	// A SHA-1 ssh-rsa signature is refused even from an authorized key.
	sha1, err := signer.(ssh.AlgorithmSigner).SignWithAlgorithm(rand.Reader, SignedData(msg), ssh.KeyAlgoRSA)
	if err != nil {
		t.Fatal(err)
	}
	weak := proto.KeyProof{PublicKey: signer.PublicKey().Marshal(), Signature: ssh.Marshal(sha1)}
	if _, err := ks.Verify([]proto.KeyProof{weak}, msg, nil, time.Now()); err == nil {
		t.Fatal("ssh-rsa (SHA-1) signature accepted")
	}
}

// userCert issues a user certificate for key, signed by ca.
func userCert(t *testing.T, ca, key ssh.Signer, principals []string, mutate func(*ssh.Certificate)) ssh.Signer {
	t.Helper()
	cert := &ssh.Certificate{
		Key:             key.PublicKey(),
		KeyId:           "alice-2026",
		CertType:        ssh.UserCert,
		ValidPrincipals: principals,
		ValidAfter:      uint64(time.Now().Add(-time.Hour).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if mutate != nil {
		mutate(cert)
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	cs, err := ssh.NewCertSigner(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestCertificateAuthority(t *testing.T) {
	ca, otherCA, alice := newEd25519(t), newEd25519(t), newEd25519(t)
	ks := mustStore(t, authorizedLine(ca, `cert-authority,principals="alice,bob"`, "corp-ca"))
	msg := HelloMessage([]byte("e"), proto.SessionID{})
	now := time.Now()
	check := func(s ssh.Signer, remote net.IP) (KeyIdentity, error) {
		return ks.Verify([]proto.KeyProof{prove(t, s, msg)}, msg, remote, now)
	}

	id, err := check(userCert(t, ca, alice, []string{"alice"}, nil), nil)
	if err != nil {
		t.Fatalf("valid certificate refused: %v", err)
	}
	// The identity is the key inside the certificate, so renewing the
	// certificate keeps the same sessions.
	if id.ID != KeyIDPrefix+ssh.FingerprintSHA256(alice.PublicKey()) || id.Label != "cert:alice-2026" {
		t.Fatalf("identity = %+v", id)
	}

	// The bare key is not authorized; only certificates signed by the CA are.
	if _, err := check(alice, nil); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bare key under a CA line: err = %v", err)
	}
	refusals := map[string]ssh.Signer{
		"other CA":              userCert(t, otherCA, alice, []string{"alice"}, nil),
		"principal not allowed": userCert(t, ca, alice, []string{"mallory"}, nil),
		"expired":               userCert(t, ca, alice, []string{"alice"}, func(c *ssh.Certificate) { c.ValidBefore = uint64(now.Add(-time.Minute).Unix()) }),
		"host certificate":      userCert(t, ca, alice, []string{"alice"}, func(c *ssh.Certificate) { c.CertType = ssh.HostCert }),
		"unknown critical option": userCert(t, ca, alice, []string{"alice"}, func(c *ssh.Certificate) {
			c.CriticalOptions = map[string]string{"verify-required": ""}
		}),
	}
	for name, s := range refusals {
		if _, err := check(s, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// source-address is enforced against the client's UDP address.
	pinned := userCert(t, ca, alice, []string{"alice"}, func(c *ssh.Certificate) {
		c.CriticalOptions = map[string]string{"source-address": "192.0.2.0/24,2001:db8::1"}
	})
	if _, err := check(pinned, net.ParseIP("192.0.2.7")); err != nil {
		t.Errorf("source-address inside the range refused: %v", err)
	}
	if _, err := check(pinned, net.ParseIP("2001:db8::1")); err != nil {
		t.Errorf("source-address exact match refused: %v", err)
	}
	if _, err := check(pinned, net.ParseIP("198.51.100.1")); err == nil {
		t.Error("source-address outside the range accepted")
	}
}

func TestKeyStoreReloadKeepsPreviousOnError(t *testing.T) {
	alice, bob := newEd25519(t), newEd25519(t)
	path := filepath.Join(t.TempDir(), "authorized_keys")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(authorizedLine(alice, "", "alice") + "\n" + authorizedLine(bob, "", "bob") + "\n")
	ks, err := LoadKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := ks.Authorize(bob.PublicKey(), nil, now); err != nil {
		t.Fatal(err)
	}

	// Revoking bob is deleting his line and reloading.
	write(authorizedLine(alice, "", "alice") + "\n")
	if err := ks.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Authorize(bob.PublicKey(), nil, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked key still authorized: %v", err)
	}

	// A broken edit keeps the last good set rather than locking alice out.
	write("ssh-ed25519 truncated-in-the-middle-of-an-edit")
	if err := ks.Reload(); err == nil {
		t.Fatal("broken file loaded")
	}
	if _, err := ks.Authorize(alice.PublicKey(), nil, now); err != nil {
		t.Fatalf("previous set lost after a failed reload: %v", err)
	}
}
