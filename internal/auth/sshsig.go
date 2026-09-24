package auth

import (
	"crypto/sha512"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"

	"github.com/0x0079/tingly-shell/internal/proto"
)

// SigNamespace scopes every signature this project asks an SSH key for. It
// keeps our signatures apart from other SSHSIG users (git, file signing), and
// the SSHSIG framing itself keeps them apart from SSH user authentication
// (.design/ssh-key-auth.pencil.md §2.4).
const SigNamespace = "tingly-shell-hello-v1"

// helloContext prefixes the binding message so it cannot be confused with any
// other message this project might one day sign.
const helloContext = "tingly-shell hello v1"

// HelloMessage is what a key signs to open a link: the TLS exporter ties the
// signature to this one connection, so a signature relayed by a man in the
// middle does not verify on the real server, and the session id ties it to
// one session.
func HelloMessage(exporter []byte, id proto.SessionID) []byte {
	m := make([]byte, 0, len(helloContext)+1+len(exporter)+len(id))
	m = append(m, helloContext...)
	m = append(m, 0)
	m = append(m, exporter...)
	return append(m, id[:]...)
}

// SignedData wraps a message in the OpenSSH SSHSIG structure, which is what
// actually gets signed (PROTOCOL.sshsig). Signing this rather than the raw
// message is what makes it safe to ask an SSH key for the signature: the
// "SSHSIG" magic can never parse as SSH userauth data.
func SignedData(message []byte) []byte {
	digest := sha512.Sum512(message)
	b := []byte("SSHSIG")
	b = appendSSHString(b, []byte(SigNamespace))
	b = appendSSHString(b, nil) // reserved
	b = appendSSHString(b, []byte("sha512"))
	return appendSSHString(b, digest[:])
}

func appendSSHString(b, s []byte) []byte {
	n := len(s)
	b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	return append(b, s...)
}

// errWeakAlgorithm refuses signature algorithms we do not accept even if the
// key itself is authorized.
var errWeakAlgorithm = errors.New("auth: weak signature algorithm")

// checkAlgorithms refuses DSA keys and SHA-1 RSA signatures, matching what
// current OpenSSH accepts by default.
func checkAlgorithms(pub ssh.PublicKey, sig *ssh.Signature) error {
	key := pub
	if cert, ok := pub.(*ssh.Certificate); ok {
		key = cert.Key
	}
	switch key.Type() {
	case "ssh-dss":
		return fmt.Errorf("%w: %s keys", errWeakAlgorithm, key.Type())
	case ssh.KeyAlgoRSA:
		if sig.Format != ssh.KeyAlgoRSASHA256 && sig.Format != ssh.KeyAlgoRSASHA512 {
			return fmt.Errorf("%w: RSA signature %q, want rsa-sha2-256 or rsa-sha2-512", errWeakAlgorithm, sig.Format)
		}
	}
	return nil
}

// VerifySignature checks one proof's signature over message. It does not
// decide whether the key is allowed; KeyStore does that.
func VerifySignature(pub ssh.PublicKey, sigBlob, message []byte) error {
	sig := new(ssh.Signature)
	if err := ssh.Unmarshal(sigBlob, sig); err != nil {
		return fmt.Errorf("auth: parse signature: %w", err)
	}
	if err := checkAlgorithms(pub, sig); err != nil {
		return err
	}
	if err := pub.Verify(SignedData(message), sig); err != nil {
		return fmt.Errorf("auth: bad signature: %w", err)
	}
	return nil
}
