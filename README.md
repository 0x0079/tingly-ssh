# tingly-shell

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

# 生成预共享 token（两端同一份，权限必须 0600）
./tingly-shell keygen > token && chmod 600 token

# 服务端：和 sshd 同机。启动日志会打印 pin=sha256:...
./tingly-shell server --listen :7443 --target 127.0.0.1:22 --token-file token

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

关键参数：`--session-linger`（断网可恢复时长，默认 60s）、`--window`（每流窗口，
同时决定重放内存上界）、`--idle-timeout` / `--keepalive`（多快判定链路已死）。

## 文档

设计先行，代码跟随。全部设计文档在 [`docs/`](docs/README.md)：

- [目标与范围](docs/00-vision-and-scope.md) · [架构](docs/01-architecture.md) · [线格式规范](docs/02-wire-protocol.md)
- [会话恢复语义](docs/03-session-resumption.md) · [安全模型](docs/04-security-model.md)
- [里程碑与进度](docs/05-roadmap.md) · [测试策略](docs/06-testing.md)
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
go test -race ./...     # 单元 + 会话层故障注入 + 真 QUIC 端到端
./test/e2e/run.sh       # 真 sshd + 真 ssh/scp 的端到端验收（约 3 分钟）
```

- 会话层测试用 `net.Pipe` 做链路，在传输途中反复切断，逐字节比对恢复后的数据流。
- `test/e2e/` 自建一个独立 sshd 并用 `-F` 配置接入 ssh，**不改动系统和用户的 SSH 配置**，
  覆盖 ProxyCommand、本地端口、scp 完整性、并发会话、链路销毁、30 秒网络黑洞、
  linger 超时、token 与 pin 拒绝共 11 个用例，日志与结果归档到 `test/e2e/artifacts/`。
- 完整验收清单（含真机漫游手工用例）：[`docs/07-verification-plan.md`](docs/07-verification-plan.md)。

## 当前状态

M0–M2 完成（设计文档、会话层、QUIC 承载、bridge、CLI、测试）。
**v0 已知限制**：进程重启后会话不恢复；客户端主动换网尚未接入 quic-go 的 Path API；
token 是 TLS 内的 bearer token。逐条跟踪在 [roadmap](docs/05-roadmap.md)。
