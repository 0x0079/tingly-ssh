package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// startAgent serves an in-memory keyring on a unix socket, speaking the real
// agent protocol, and returns the socket path.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()
	keyring := agent.NewKeyring()
	for _, k := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	// Unix socket paths are short; t.TempDir() can exceed the limit.
	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = agent.ServeAgent(keyring, c)
			}()
		}
	}()
	return sock
}

func newRawKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func TestAgentProverOffersEveryAgentKey(t *testing.T) {
	k1, _ := newRawKey(t)
	k2, p2 := newRawKey(t)
	sock := startAgent(t, k1, k2)

	prover, err := NewAgentProver(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	pubs, err := prover.Keys()
	if err != nil || len(pubs) != 2 {
		t.Fatalf("keys = %d, %v", len(pubs), err)
	}

	// Only the second key is authorized; the proof set still gets it in.
	ks := mustStore(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(p2)))+" second")
	msg := HelloMessage([]byte("exporter"), proto.SessionID{9})
	proofs, err := prover.Prove(msg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ks.Verify(proofs, msg, nil, time.Now())
	if err != nil || id.Label != "second" {
		t.Fatalf("verify = %+v, %v", id, err)
	}
}

func TestAgentProverWithIdentity(t *testing.T) {
	k1, _ := newRawKey(t)
	k2, p2 := newRawKey(t)
	sock := startAgent(t, k1, k2)
	dir := t.TempDir()

	// A .pub selects exactly that key from the agent.
	pubPath := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(p2), 0o644); err != nil {
		t.Fatal(err)
	}
	prover, err := NewAgentProver(sock, pubPath)
	if err != nil {
		t.Fatal(err)
	}
	pubs, err := prover.Keys()
	if err != nil || len(pubs) != 1 || string(pubs[0].Marshal()) != string(p2.Marshal()) {
		t.Fatalf("identity selected %d keys, %v", len(pubs), err)
	}

	// A key that is not in the agent is a clear error, with the fix.
	_, stray := newRawKey(t)
	strayPath := filepath.Join(dir, "stray.pub")
	if err := os.WriteFile(strayPath, ssh.MarshalAuthorizedKey(stray), 0o644); err != nil {
		t.Fatal(err)
	}
	prover, err = NewAgentProver(sock, strayPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prover.Keys(); err == nil || !strings.Contains(err.Error(), "ssh-add") {
		t.Fatalf("missing agent key: err = %v", err)
	}
}

func TestIdentityFileWithoutAgent(t *testing.T) {
	priv, pub := newRawKey(t)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unencrypted private key needs no agent at all.
	prover, err := NewAgentProver("", path)
	if err != nil {
		t.Fatal(err)
	}
	pubs, err := prover.Keys()
	if err != nil || len(pubs) != 1 || string(pubs[0].Marshal()) != string(pub.Marshal()) {
		t.Fatalf("keys = %v, %v", pubs, err)
	}

	// An encrypted one does, and says so.
	block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentProver("", path); err == nil || !strings.Contains(err.Error(), "ssh-agent") {
		t.Fatalf("encrypted key without agent: err = %v", err)
	}
}

func TestAgentProverNeedsAnAgent(t *testing.T) {
	if _, err := NewAgentProver("", ""); err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("err = %v", err)
	}
	sock := startAgent(t) // running, but empty
	prover, err := NewAgentProver(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prover.Keys(); err == nil || !strings.Contains(err.Error(), "ssh-add") {
		t.Fatalf("empty agent: err = %v", err)
	}
}
