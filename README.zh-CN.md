# tingly-shell

[English](README.md) | 简体中文

**SSH over QUIC with session resumption.** 不改 OpenSSH、不改 sshd，在两端各放一个用户态
bridge，中间跑 QUIC + 可恢复会话层，让 SSH 连接在 Wi-Fi ↔ 蜂窝切换、NAT rebinding、
甚至短时断网之后继续存活。

```
OpenSSH client ─TCP/stdio─▶ tingly-shell client ─QUIC─▶ tingly-shell server ─TCP─▶ sshd
                                    └── Resumable Session Layer ──┘
```

职责分层（架构的核心约束）：

| 层 | 负责 |
| --- | --- |
| SSH | 认证、端到端加密、channel 语义 |
| Session 层（本项目） | `session_id`、绝对字节偏移量、重放、去重、跨连接重连、逻辑流多路复用 |
| QUIC（quic-go） | 握手加密、拥塞控制、丢包恢复、连接迁移、NAT rebinding |

一句话：**QUIC 解决"活着的连接换了地址"，Session 层解决"连接死了又活过来"。**

## 快速开始

```bash
go build ./cmd/tingly-shell

# 每台设备一份凭据：token 给设备，stderr 上打印的记录行加进服务端的凭据文件
# （服务端只存哈希，不存 token 本身）
./tingly-shell keygen --label laptop-mbp14 > token && chmod 600 token

# 服务端：和 sshd 同机。启动日志会打印 pin=sha256:...
./tingly-shell server --listen :7443 --target 127.0.0.1:22 --credentials credentials

# 客户端 A：本地端口转发
./tingly-shell client --server SERVER:7443 --listen 127.0.0.1:2222 \
    --token-file token --pin sha256:...
ssh -p 2222 user@127.0.0.1

# 客户端 B：ProxyCommand（推荐，无本地监听端口）
ssh -o ProxyCommand="./tingly-shell proxy --server SERVER:7443 --token-file token --pin sha256:..." user@host
```

跳板机场景（`笔记本 → 跳板机 → 目标机`，**跳板机零改动**）：把第一跳套进隧道，
其余照常用 OpenSSH 的 `ProxyJump`。只有第一跳会因为换网而断，保护它就够了。
配置片段与边界条件见 [`docs/08-jump-host-topologies.md`](docs/08-jump-host-topologies.md)。

`--pin` 和 `--token-file` 回答的是两个不同的问题，两个都要配。pin 是服务端公钥的指纹，
公开信息，证明"连对了机器"；token 才是真正的密码，证明"你有资格接入"。
每台设备一份 token，所以撤销一台只需删掉一行再 `SIGHUP`；会话绑定到创建它的凭据，
日志里也能看出是哪台设备。
区别与常见误区见 [`docs/04-security-model.md`](docs/04-security-model.md) §2.1。
同一份文档还有威胁模型（§6）、各类凭据的生命周期与有效期（§7）、
已知风险清单与处置状态（§8）、部署加固清单（§9）。

关键参数：`--session-linger`（断网可恢复时长，默认 60s）、`--window`（每流窗口，
同时决定重放内存上界）、`--idle-timeout` / `--keepalive`（多快判定链路已死），
服务端还有 `--max-sessions`，它决定内存上界。启动日志会打印最坏内存，请按实际并发调整，
不要直接用默认值。

## 文档

设计先行，代码跟随。全部设计文档在 [`docs/`](docs/README.md)：

- [目标与范围](docs/00-vision-and-scope.md) · [架构](docs/01-architecture.md) · [线格式规范](docs/02-wire-protocol.md)
- [会话恢复语义](docs/03-session-resumption.md) · [安全模型](docs/04-security-model.md)
- [里程碑与进度](docs/05-roadmap.md) · [测试策略](docs/06-testing.md) · [验收方案](docs/07-verification-plan.md)
- [跳板机/多跳拓扑](docs/08-jump-host-topologies.md)
- ADR：[传输层选型（QUIC vs MPTCP vs RFC 8803）](docs/adr/0001-transport-choice.md) ·
  [为什么还要会话层](docs/adr/0002-resumable-session-layer.md) ·
  [依赖选型调研](docs/adr/0003-library-choices.md)

## 代码结构

| 目录 | 内容 |
| --- | --- |
| `internal/proto` | `tingly/0` 帧编解码（基于 quic-go 的 `quicvarint`） |
| `internal/mux` | 可恢复会话层：Session / Stream / 重放缓冲 / 偏移量流控 |
| `internal/transport` | QUIC dial/listen、TLS、自签证书、SPKI pin、token 加载 |
| `internal/bridge` | client（TCP/stdio 接入 + 重连 supervisor）、server（会话注册表 + 目标白名单） |
| `cmd/tingly-shell` | `server` / `client` / `proxy` / `keygen` 四个子命令 |

直接依赖只有一个：`github.com/quic-go/quic-go`。其余全部用标准库，理由见 ADR-0003。

## 验证

```bash
go test -race ./...              # 单元 + 会话层故障注入 + 真 QUIC
./test/e2e/run.sh                # 真 sshd + 真 ssh/scp 端到端（14 个用例）
./test/scenarios/run.sh          # 日常 SSH 使用场景（17 个用例）
./test/roaming/selftest.sh       # 漫游套件 + 模拟故障，自证 harness（7 个用例）
./test/roaming/run.sh            # 同一套件跑真实部署与真实无线电
```

每个套件都自建独立 sshd 并用 `-F` 配置接入 ssh，**不改动系统和用户的 SSH 配置**。
日志、transcript 与结果表按次归档到各套件的 `artifacts/`。

- **会话层**：用 `net.Pipe` 做链路，在传输途中反复切断，逐字节比对恢复后的数据流。
- **端到端**（`test/e2e/`）：ProxyCommand、本地端口、stdin EOF、scp 完整性、并发会话、
  链路销毁、30 秒网络黑洞、linger 超时、token 与 pin 拒绝、跳板机两跳、
  每设备凭据与撤销。
- **日常 SSH**（`test/scenarios/`）：真 pty 上的交互式 shell、断链前后在同一个 shell 里
  继续敲命令、32 MiB 标准输出、全部 256 种字节值、Ctrl-C、窗口尺寸变化、
  `-L`/`-R`/`-D` 转发、sftp、rsync、ControlMaster 复用只占一条隧道流，
  以及三个 tmux 用例把两件事分清：隧道让 tmux 会话在链路被销毁时不用重新 attach，
  tmux 兜住连接彻底死掉时远端的工作。
- **漫游**（`test/roaming/`）：离开与回到网络、linger 两侧的断网、NAT 空闲、
  传输中途换网、服务端重启。远端心跳让结果可判定：计数器连续性证明字节流没被破坏，
  最大到达间隔就是终端实际卡顿的时长。无线电通过 nmcli 或 macOS 自动控制，
  没有这些则提示操作者，CI 里用模拟故障。
- 完整验收清单：[`docs/07-verification-plan.md`](docs/07-verification-plan.md)。

## 当前状态

M0–M2 完成（设计文档、会话层、QUIC 承载、bridge、CLI、测试）。
**v0 已知限制**：进程重启后会话不恢复；客户端主动换网尚未接入 quic-go 的 Path API；
token 是 TLS 内的 bearer token。逐条跟踪在 [roadmap](docs/05-roadmap.md)。
