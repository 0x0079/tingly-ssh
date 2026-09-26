package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/0x0079/tingly-ssh/internal/proto"
)

// Prover signs a HELLO's binding message with the user's existing SSH keys.
// It is the client half of .design/ssh-key-auth.pencil.md §6.
type Prover struct {
	// signers returns the keys to sign with and a function that releases
	// whatever they hold open (the agent connection).
	signers func() ([]ssh.Signer, func(), error)
	desc    string
}

// NewSignerProver signs with keys already in memory.
func NewSignerProver(signers ...ssh.Signer) *Prover {
	return &Prover{
		signers: func() ([]ssh.Signer, func(), error) { return signers, func() {}, nil },
		desc:    fmt.Sprintf("%d in-memory key(s)", len(signers)),
	}
}

// NewAgentProver uses the agent at sock. With no identity it offers every
// agent key except FIDO ones, which would need a touch per key per link; with
// an identity it offers only the matching key (and its certificate, if the
// agent holds one).
//
// identity may name an unencrypted private key, which is then used directly
// and the agent is not needed, or an encrypted private key or a .pub file,
// whose key must be loaded in the agent.
func NewAgentProver(sock, identity string) (*Prover, error) {
	if identity != "" {
		signer, pub, err := loadIdentity(identity)
		if err != nil {
			return nil, err
		}
		if signer != nil {
			p := NewSignerProver(signer)
			p.desc = identity
			return p, nil
		}
		if sock == "" {
			return nil, fmt.Errorf("auth: %s is encrypted or hardware-backed, so it must be in ssh-agent, but SSH_AUTH_SOCK is not set", identity)
		}
		want := pub.Marshal()
		return &Prover{
			signers: func() ([]ssh.Signer, func(), error) {
				all, release, err := agentSigners(sock)
				if err != nil {
					return nil, nil, err
				}
				var out []ssh.Signer
				for _, s := range all {
					if keyMatches(s.PublicKey(), want) {
						out = append(out, s)
					}
				}
				if len(out) == 0 {
					release()
					return nil, nil, fmt.Errorf("auth: %s is not loaded in ssh-agent; run ssh-add %s", identity, strings.TrimSuffix(identity, ".pub"))
				}
				return out, release, nil
			},
			desc: identity + " via ssh-agent",
		}, nil
	}
	if sock == "" {
		return nil, errors.New("auth: no key to authenticate with: SSH_AUTH_SOCK is not set; start ssh-agent and ssh-add a key, or pass --identity or --token-file")
	}
	return &Prover{
		signers: func() ([]ssh.Signer, func(), error) {
			all, release, err := agentSigners(sock)
			if err != nil {
				return nil, nil, err
			}
			var out []ssh.Signer
			for _, s := range all {
				if isSecurityKey(s.PublicKey()) {
					continue
				}
				if len(out) == proto.MaxKeyProofs {
					break
				}
				out = append(out, s)
			}
			if len(out) == 0 {
				release()
				return nil, nil, errors.New("auth: ssh-agent holds no usable key (FIDO keys need --identity); run ssh-add")
			}
			return out, release, nil
		},
		desc: "ssh-agent",
	}, nil
}

// String describes where keys come from, for logs.
func (p *Prover) String() string { return p.desc }

// Keys lists the public keys Prove would sign with. The CLI calls it at
// startup so a missing agent or key fails fast instead of retrying until the
// session linger runs out.
func (p *Prover) Keys() ([]ssh.PublicKey, error) {
	signers, release, err := p.signers()
	if err != nil {
		return nil, err
	}
	defer release()
	out := make([]ssh.PublicKey, len(signers))
	for i, s := range signers {
		out[i] = s.PublicKey()
	}
	return out, nil
}

// Prove signs message with every key and returns the proofs. A key that fails
// to sign (an agent confirmation refused, a security key not touched) is
// skipped; only when none signs is it an error.
func (p *Prover) Prove(message []byte) ([]proto.KeyProof, error) {
	signers, release, err := p.signers()
	if err != nil {
		return nil, err
	}
	defer release()
	data := SignedData(message)
	var proofs []proto.KeyProof
	var errs []error
	for _, s := range signers {
		sig, err := sign(s, data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ssh.FingerprintSHA256(s.PublicKey()), err))
			continue
		}
		proofs = append(proofs, proto.KeyProof{PublicKey: s.PublicKey().Marshal(), Signature: ssh.Marshal(sig)})
	}
	if len(proofs) == 0 {
		return nil, fmt.Errorf("auth: no key produced a signature: %w", errors.Join(errs...))
	}
	return proofs, nil
}

// sign asks for SHA-512 RSA signatures: the default for an RSA key is SHA-1
// ssh-rsa, which servers refuse.
func sign(s ssh.Signer, data []byte) (*ssh.Signature, error) {
	switch s.PublicKey().Type() {
	case ssh.KeyAlgoRSA, ssh.CertAlgoRSAv01:
		as, ok := s.(ssh.AlgorithmSigner)
		if !ok {
			return nil, errors.New("RSA key cannot produce rsa-sha2-512 signatures")
		}
		return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA512)
	default:
		return s.Sign(rand.Reader, data)
	}
}

func agentSigners(sock string) ([]ssh.Signer, func(), error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: connect to ssh-agent at %s: %w", sock, err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("auth: list ssh-agent keys: %w", err)
	}
	return signers, func() { conn.Close() }, nil
}

// loadIdentity returns a signer when path is an unencrypted private key, and
// otherwise the public key to look for in the agent.
func loadIdentity(path string) (ssh.Signer, ssh.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: identity: %w", err)
	}
	if pub, _, _, _, err := ssh.ParseAuthorizedKey(raw); err == nil {
		return nil, pub, nil
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err == nil {
		return signer, signer.PublicKey(), nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) && missing.PublicKey != nil {
		return nil, missing.PublicKey, nil
	}
	// Hardware-backed keys and old PEM files do not reveal their public key;
	// the .pub next to them does.
	if pubRaw, pubErr := os.ReadFile(path + ".pub"); pubErr == nil {
		if pub, _, _, _, err := ssh.ParseAuthorizedKey(pubRaw); err == nil {
			return nil, pub, nil
		}
	}
	return nil, nil, fmt.Errorf("auth: identity %s: %w", path, err)
}

// keyMatches reports whether an agent key is the wanted key, or a certificate
// for it.
func keyMatches(have ssh.PublicKey, want []byte) bool {
	if bytes.Equal(have.Marshal(), want) {
		return true
	}
	cert, ok := have.(*ssh.Certificate)
	return ok && bytes.Equal(cert.Key.Marshal(), want)
}

func isSecurityKey(pub ssh.PublicKey) bool {
	return strings.HasPrefix(pub.Type(), "sk-")
}
