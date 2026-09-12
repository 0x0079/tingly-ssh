# 00 · 目标与范围

## 1. 问题陈述

SSH 的 transport 层（RFC 4253）假设底下是一条可靠、有序的 TCP 字节流。TCP 连接被
四元组 `(src IP, src port, dst IP, dst port)` 绑定，因此：

- 手机/笔记本从 Wi-Fi 切到 5G → src IP 改变 → 连接断；
- NAT 超时重绑定 → 连接断；
- 地铁隧道断网 30 秒 → 内核重传超时 → 连接断。

结果是用户熟悉的 `Connection reset by peer` 和重新 `ssh` + 重开 tmux。

## 2. 设计目标（v0 验收标准）

| # | 目标 | 验收标准 |
| --- | --- | --- |
| G1 | 不改 OpenSSH / sshd | 客户端用原生 `ssh`，服务端用发行版 `sshd`，零 patch |
| G2 | 换网不断连 | 客户端源 IP/端口变化后，已建立的 SSH 会话继续可用，无需重新认证 |
| G3 | 短时断网可恢复 | 默认 60s（可配到 10min）内网络恢复，SSH 会话继续，字节流不丢不重 |
| G4 | 端到端安全不降级 | SSH 自身的认证与加密完全保留；隧道层是额外一层，不是替代 |
| G5 | 单二进制、跨平台 | `go build` 出一个二进制，含 client / server / proxy 三种模式 |
| G6 | 成熟依赖 | 传输层用 `quic-go`，不自研拥塞控制、不自研加密握手 |

## 3. 非目标（v0 明确不做）

- **不做进程级持久化**：client/server 进程重启后会话不恢复（会话状态在内存）。
  详见 `05-roadmap.md` M4。
- **不做多路径并发聚合**（同时用 Wi-Fi + 5G 提升带宽）。QUIC multipath 仍在标准化中，
  见 `adr/0001-transport-choice.md`。
- **不做 SSH 协议感知**：隧道不解析 SSH 报文，不做 channel 级重连（那是 mosh 的路线）。
- **不做端口转发策略引擎**：服务端只允许一组白名单目标地址。
- **不替代 VPN**：只转发 TCP 流，不转发 IP 包。

## 4. 用户故事

- **US1**：我在咖啡店用 Wi-Fi 连公司跳板机跑编译，出门切 5G，终端里的日志继续滚。
- **US2**：我在地铁里丢网 40 秒，出站后终端还在，不用重连 tmux。
- **US3**：我不想在服务器上开新端口给未知协议 → 我只需要 UDP/443，并用 token + 证书 pin 限制接入。
- **US4**：我希望用标准 `ssh` 命令：`ssh -o ProxyCommand='tingly-shell proxy ...' host`。

## 5. 与现有方案的关系

| 方案 | 关系 |
| --- | --- |
| **mosh** | 思路近（UDP + 状态同步），但 mosh 替换了终端语义（SSP 同步屏幕），不透明转发 TCP，不支持端口转发/scp。我们选择透明字节流。 |
| **MPTCP (RFC 8684)** | 内核层最"标准"的答案，但需要两端 OS 支持；不支持时静默退化。见 ADR-0001。 |
| **Transport Converter (RFC 8803)** | 我们的 bridge 架构在概念上就是一对 converter：legacy TCP ↔ 新传输 ↔ legacy TCP。ADR-0001 引用它作为架构背书。 |
| **WireGuard + 漫游** | WireGuard 的 roaming 解决的是 IP 变化，但 TCP 连接本身仍然会因为断网超时而死；且需要部署 VPN。 |
| **Tailscale SSH / Cloudflare Access** | 产品化的托管方案，依赖控制面。我们要的是自托管、单二进制、无控制面。 |
