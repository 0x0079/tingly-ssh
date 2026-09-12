# ADR-0003 · 依赖选型（调研结论）

- 状态：**已接受**
- 日期：2026-09-12
- 原则：传输、加密、编码这类有成熟实现的一律不自研；只自研别人没有的那一层（跨连接的会话恢复语义）。

## 1. 最终选择（直接依赖只有 1 个）

| 需求 | 选择 | 版本 | 许可 |
| --- | --- | --- | --- |
| QUIC 传输 | `github.com/quic-go/quic-go` | v0.62.0 | MIT |
| 变长整数编码 | `quic-go/quicvarint`（同仓库子包） | 同上 | MIT |
| TLS 1.3 / 自签证书 / SPKI pin | 标准库 `crypto/tls`、`crypto/x509`、`crypto/ed25519` | — | — |
| token 比较 / 未来 HMAC | 标准库 `crypto/subtle`、`crypto/hmac` | — | — |
| 日志 | 标准库 `log/slog` | — | — |
| CLI | 标准库 `flag` + 子命令分发 | — | — |
| 测试 | 标准库 `testing` + `net.Pipe` 故障注入 | — | — |

`go.mod` 的 `require` 里只有 quic-go（`golang.org/x/crypto|net|sys` 是它的间接依赖）。

## 2. QUIC 实现调研

| 候选 | 结论 | 理由 |
| --- | --- | --- |
| **quic-go/quic-go** | **采用** | Go 生态事实标准（Caddy、Traefik、containerd 生态在用）；RFC 9000/9001/9002 完整实现，附带 PMTUD(RFC 8899)、ECN、GSO、地址验证 Retry、qlog；**提供 `Conn.AddPath(*Transport)` + `Path.Probe` + `Path.Switch`**，即标准路径迁移的可编程接口 |
| `golang.org/x/net/quic` | 不采用 | 官方实验实现，API 未稳定、未承诺兼容，功能面小于 quic-go |
| `lucas-clemente/quic-go` | 同一项目旧路径 | 已改名为 `quic-go/quic-go` |
| 各类混淆 fork（Psiphon、hysteria 内置栈等） | 不采用 | 为抗审查场景定制，跟进上游滞后；我们不需要混淆 |
| `xtaci/kcp-go`（KCP over UDP） | 不采用，保留作为备选 | 非标准协议，无 TLS 1.3 集成、无连接迁移语义；仅在"UDP 可用但 QUIC 被 DPI 干扰"时作为备选通道评估（未列入 v0） |
| 自研 UDP 可靠层 | 否决 | 要重做拥塞控制、丢包恢复、握手加密、地址验证——这正是本项目要避免的 |

### 2.1 必须写清的 quic-go 迁移边界

| 场景 | quic-go 的能力 | 我们的做法 |
| --- | --- | --- |
| NAT rebinding（对端看到新源地址，被动） | 支持：收到新地址的包后做 path validation，连接不断 | 直接受益，会话层无感 |
| 客户端主动换网（Wi-Fi→5G，需要换本地 UDP socket） | 需要应用显式调用 `AddPath`/`Probe`/`Switch`，不会自动发生 | **v0 未接入**：换网表现为链路死亡 + 会话层重连（SSH 不断，仅停顿约一个 RTT + 握手）；接入 Path API 列入 M3，属于**优化而非正确性依赖** |
| 长时间断网后恢复 | 不支持（连接已死） | 由会话层负责（ADR-0002） |

这个边界是整套架构把"可恢复"放在独立一层的直接理由：即使 quic-go 的迁移完全不可用，产品功能也只是降级为"停顿一下"，而不是断连。

## 3. 多路复用 / 会话恢复调研

| 候选 | 结论 | 理由 |
| --- | --- | --- |
| `hashicorp/yamux`、`xtaci/smux` | 不采用 | 成熟稳定，但 session **绑死在单条底层连接**上：连接断 → session 死，没有跨连接的偏移对齐与重放，而这恰好是本项目唯一要解决的问题；叠在 QUIC 上还会形成三层流控 |
| QUIC 原生多流（一逻辑流 = 一 QUIC 流） | v0 不采用 | 重连时需要确定性重放顺序与映射重建，复杂度高；见 ADR-0002 与 roadmap M5 |
| `libp2p` / `mosh` 的 SSP | 不采用 | 前者引入巨大依赖树与自有寻址模型；后者是终端语义层同步，无法透明承载任意字节流 |
| **自研 `internal/mux`（约 600 行）** | **采用** | 只做三件事：绝对偏移量、重放对齐、逻辑流多路复用。这是市面上没有现成实现的一层 |

## 4. 其他被否决的依赖

| 候选 | 否决理由 |
| --- | --- |
| `cobra` / `urfave/cli` | 四个子命令用标准库 `flag` 足够，不值得引入依赖树 |
| `zap` / `logrus` | `log/slog` 已是标准库且结构化 |
| `testify` | 断言糖不值得成为直接依赖（注意：它出现在 `go.sum` 里是因为 quic-go 的**测试**依赖，不进我们的 `require`） |
| `certmagic` / ACME 客户端 | v0 用自签证书 + SPKI pin；要 ACME 的用户可自行提供 `--cert/--key` |
| JWT / OAuth 库 | 认证是 PSK token（M3 改 HMAC 挑战-响应），不需要令牌格式框架 |
| 退避/重试库 | 十几行标准库代码 |

## 5. 依赖纪律

- 新增**直接**依赖必须先改本 ADR（写清候选、否决理由、许可）。
- 不引入只为省几十行代码的工具库。
- Go 版本要求：quic-go v0.62 要求 `go 1.26`。若需支持更老的 Go，回退到 quic-go v0.48–v0.54 并适配 API（`quic.Connection` → `*quic.Conn` 等重命名）；这是已知的升级成本，记录在此。
