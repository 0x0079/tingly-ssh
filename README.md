# tingly-shell

English | [简体中文](README.zh-CN.md)

**SSH over QUIC with session resumption.** No changes to OpenSSH or sshd: a user-space
bridge sits at each end with QUIC and a resumable session layer in between, so an SSH
connection survives a Wi-Fi to cellular switch, NAT rebinding, or a brief outage.

```
OpenSSH client ─TCP/stdio─▶ tingly-shell client ─QUIC─▶ tingly-shell server ─TCP─▶ sshd
                                    └── Resumable Session Layer ──┘
```

Layering is the central design constraint:

| Layer | Owns |
| --- | --- |
| SSH | Authentication, end-to-end encryption, channel semantics |
| Session layer (this project) | `session_id`, absolute byte offsets, replay, dedup, reconnection across links, logical stream multiplexing |
| QUIC (quic-go) | Handshake encryption, congestion control, loss recovery, connection migration, NAT rebinding |

In one line: **QUIC handles a live connection changing address; the session layer handles
a connection that died and came back.**

## Quick start

```bash
go build ./cmd/tingly-shell

# Pre-shared token, identical on both ends, mode 0600
./tingly-shell keygen > token && chmod 600 token

# Server, next to sshd. Its startup log prints pin=sha256:...
./tingly-shell server --listen :7443 --target 127.0.0.1:22 --token-file token

# Client A: local port forward
./tingly-shell client --server SERVER:7443 --listen 127.0.0.1:2222 \
    --token-file token --pin sha256:...
ssh -p 2222 user@127.0.0.1

# Client B: ProxyCommand, recommended, no local listening port
ssh -o ProxyCommand="./tingly-shell proxy --server SERVER:7443 --token-file token --pin sha256:..." user@host
```

`--pin` and `--token-file` answer different questions and you need both. The pin is the
fingerprint of the server's public key, it is public and proves you reached the right
server. The token is the actual secret and proves you are allowed in. Details and common
misconceptions: [`docs/04-security-model.md`](docs/04-security-model.md) §2.1.

**Jump hosts** (`laptop → jump host → target`, with **no changes on the jump host**):
tunnel the first hop and use stock OpenSSH `ProxyJump` for the rest. Only the first hop
breaks when the laptop changes network, so protecting it is enough. Config snippets and
the cases where this cannot work: [`docs/08-jump-host-topologies.md`](docs/08-jump-host-topologies.md).

Key flags: `--session-linger` (how long an outage stays recoverable, default 60s),
`--window` (per-stream window, which also bounds replay memory), `--idle-timeout` and
`--keepalive` (how fast a dead link is detected).

## Documentation

Design first, code second. Everything lives in [`docs/`](docs/README.md) (written in
Chinese; this README is the English entry point):

- [Scope](docs/00-vision-and-scope.md) · [Architecture](docs/01-architecture.md) · [Wire protocol](docs/02-wire-protocol.md)
- [Resumption semantics](docs/03-session-resumption.md) · [Security model](docs/04-security-model.md)
- [Roadmap](docs/05-roadmap.md) · [Test strategy](docs/06-testing.md) · [Verification plan](docs/07-verification-plan.md)
- [Jump host topologies](docs/08-jump-host-topologies.md)
- ADRs: [transport choice, QUIC vs MPTCP vs RFC 8803](docs/adr/0001-transport-choice.md) ·
  [why a session layer above QUIC](docs/adr/0002-resumable-session-layer.md) ·
  [library survey](docs/adr/0003-library-choices.md)

## Layout

| Directory | Contents |
| --- | --- |
| `internal/proto` | `tingly/0` frame codec, built on quic-go's `quicvarint` |
| `internal/mux` | Resumable session layer: Session, Stream, replay buffer, offset-based flow control |
| `internal/transport` | QUIC dial/listen, TLS, self-signed certificates, SPKI pinning, token loading |
| `internal/bridge` | Client (TCP/stdio entry plus reconnect supervisor), server (session registry plus target allowlist) |
| `cmd/tingly-shell` | Subcommands `server`, `client`, `proxy`, `keygen` |

One direct dependency: `github.com/quic-go/quic-go`. Everything else is the standard
library; the reasoning is in ADR-0003.

## Verification

```bash
go test -race ./...     # unit, session-layer fault injection, real QUIC end to end
./test/e2e/run.sh       # real sshd with real ssh/scp, about 3 minutes
```

- Session-layer tests use `net.Pipe` as the link, cut it repeatedly mid-transfer, and
  compare the recovered byte stream byte for byte.
- `test/e2e/` stands up its own sshd and reaches it through an extra `ssh -F` config, so
  **neither the system nor the user SSH configuration is touched**. Thirteen cases cover
  ProxyCommand, local port, scp integrity, concurrent sessions, link destruction, a 30s
  network black hole, the linger deadline, refused token and pin, and a two hop chain
  through an unmodified jump host. Logs and results are archived under
  `test/e2e/artifacts/`.
- Full acceptance checklist, including the manual roaming cases:
  [`docs/07-verification-plan.md`](docs/07-verification-plan.md).

## Status

M0 to M2 are done: design docs, session layer, QUIC carriage, bridges, CLI, tests.
**Known v0 limits**: a session does not survive a process restart; client-initiated
path migration does not yet use quic-go's Path API; the token is a bearer token inside
TLS. Each one is tracked in the [roadmap](docs/05-roadmap.md).
