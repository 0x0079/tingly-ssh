# tingly-shell 设计文档索引

`tingly-shell` 是一个**用户态 SSH 漫游隧道**：不修改 OpenSSH / sshd，在两端各放一个 bridge 进程，
中间跑 QUIC + 可恢复会话层（Resumable Session Layer），让 SSH 连接在 Wi-Fi ↔ 蜂窝切换、
NAT rebinding、甚至短时断网之后继续存活。

```
OpenSSH client
   │ TCP (localhost) 或 ProxyCommand stdio
   ▼
tingly-shell client  ──┐
   │                   │ Resumable Session Layer (session_id / offset / replay)
   │ QUIC (RFC 9000)   │ QUIC Connection Migration (Connection ID / path validation)
   ▼                   │
tingly-shell server  ──┘
   │ TCP (localhost)
   ▼
sshd
```

## 文档地图

| 文档 | 内容 | 状态 |
| --- | --- | --- |
| [00-vision-and-scope.md](00-vision-and-scope.md) | 目标、非目标、用户故事、验收标准 | 已定稿 (v0) |
| [01-architecture.md](01-architecture.md) | 分层模型、组件、进程与 goroutine 模型、限制 | 已定稿 (v0) |
| [02-wire-protocol.md](02-wire-protocol.md) | `tingly/0` 线格式规范（帧定义、状态机） | 已定稿 (v0) |
| [03-session-resumption.md](03-session-resumption.md) | 断链重连、replay、流控、去重的精确语义 | 已定稿 (v0) |
| [04-security-model.md](04-security-model.md) | 信任边界、TLS pin、token、威胁模型 | 已定稿 (v0) |
| [05-roadmap.md](05-roadmap.md) | 里程碑 M0..M5 与当前进度 | 持续更新 |
| [06-testing.md](06-testing.md) | 测试分层、故障注入、手工验收脚本 | 持续更新 |
| [adr/0001-transport-choice.md](adr/0001-transport-choice.md) | 为什么选 QUIC bridge 而不是 MPTCP / 改 SSH | 已接受 |
| [adr/0002-resumable-session-layer.md](adr/0002-resumable-session-layer.md) | 为什么在 QUIC 之上再加一层会话层 | 已接受 |
| [adr/0003-library-choices.md](adr/0003-library-choices.md) | 依赖选型：quic-go / quicvarint / 标准库 | 已接受 |

## 文档驱动的工作方式

1. 任何行为变更先改 `docs/`（规范 → ADR → roadmap 勾选项），再改代码。
2. 线格式的任何不兼容修改必须 bump `docs/02-wire-protocol.md` 里的 `PROTOCOL_VERSION`，
   并在 ADR 中记录迁移策略。
3. `05-roadmap.md` 是唯一的进度真相来源；代码里不写"TODO 以后做"，而是写进 roadmap。
