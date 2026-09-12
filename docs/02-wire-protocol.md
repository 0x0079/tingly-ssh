# 02 · 线格式规范 `tingly/0`

- **ALPN**：`tingly/0`
- **PROTOCOL_VERSION**：`1`（不兼容变更必须 bump，并在 ADR 记录迁移策略）
- **承载**：一条 QUIC 连接上客户端打开的**第一条双向流**。该流之外不使用其他 QUIC 流。
- **字节序/整数**：所有整数为 QUIC 变长整数（RFC 9000 §16），复用 `quic-go/quicvarint`。
- **`bytes`**：`len(varint) || raw`。

## 1. 帧结构

```
frame := frame_len(varint) || type(varint) || payload
```

`frame_len` 覆盖 `type || payload`。实现上限 `MaxFrameSize = 1 MiB`，超限视为协议错误并关闭 Link。

| type | 名称 | payload |
| --- | --- | --- |
| 0x01 | `HELLO` | `version(v) session_id(16B) epoch(v) flags(v) window(v) token(bytes) n(v) state×n` |
| 0x02 | `HELLO_ACK` | `code(v) reason(bytes) window(v) n(v) state×n` |
| 0x03 | `OPEN` | `stream_id(v) target(bytes)` |
| 0x04 | `DATA` | `stream_id(v) offset(v) data(bytes)` |
| 0x05 | `ACK` | `stream_id(v) recv_offset(v) read_offset(v)` |
| 0x06 | `FIN` | `stream_id(v) final_offset(v)` |
| 0x07 | `RESET` | `stream_id(v) code(v)` |
| 0x08 | `PING` | `nonce(v)` |
| 0x09 | `PONG` | `nonce(v)` |
| 0x0a | `CLOSE` | `code(v) reason(bytes)` |

`state`（每个活跃 Stream 一条，用于重连对齐）：

```
state := stream_id(v) recv_offset(v) read_offset(v) flags(v) target(bytes)
```

| 字段 | 含义 |
| --- | --- |
| `recv_offset` | 本端**已收下**（进入接收缓冲区）的字节数，即期望对端从此偏移继续发 |
| `read_offset` | 本端**已交付给应用**的字节数，用于流控窗口计算（`read_offset ≤ recv_offset`） |
| `flags` bit0 `FIN_RECEIVED` | 本端已收到对端 FIN |
| `flags` bit1 `FIN_SENT` | 本端已发出 FIN |
| `target` | 仅客户端侧有意义：该流的目标地址提示，使服务端可在丢失 OPEN 时重建 |

## 2. 握手

```
client                                   server
  │ ── HELLO{version, session_id, epoch, flags, window, token, states} ──▶
  │                                        验证 version / token
  │ ◀── HELLO_ACK{code=0, window, states} ──
  │  双方按 §3 对齐并开始收发
```

`HELLO.flags` bit0 `RESUME`：客户端声明"这个 session 服务端应当已经知道"（本会话此前至少成功握手过一次）。
服务端据此区分两种情况，**不能用 states 是否为空来判断**：应用可能在第一条 Link 建立之前就打开了流。

| 服务端状态 | `RESUME` | 动作 |
| --- | --- | --- |
| 未知 session | 0 | 新建 session；HELLO 里的 states 由客户端重放 `OPEN` 补齐 |
| 未知 session | 1 | 回 `SESSION_UNKNOWN`（通常是服务端重启过） |
| 已知 session | 任意 | 按 `epoch` 决定接管或 `EPOCH_STALE` |

`HELLO_ACK.code`：

| code | 含义 | 客户端动作 |
| --- | --- | --- |
| 0 | OK | 继续 |
| 1 | `UNSUPPORTED_VERSION` | 终止，不重试 |
| 2 | `UNAUTHORIZED` | 终止，不重试 |
| 3 | `SESSION_UNKNOWN` | 该 session 已过期；终止当前 Session，上层可开新 Session |
| 4 | `EPOCH_STALE` | 已有更新的 Link 接管；本 Link 静默退出 |
| 5 | `INTERNAL` | 可重试 |

`epoch` 由客户端每次重连时 +1。服务端拒绝 `epoch` 不大于当前已接管 Link 的 HELLO（防止
旧链路在网络延迟后"复活"抢占新链路）。

## 3. 重连对齐（normative）

收到对端 states 后，对每个本地 Stream：

1. 对端有该 stream：
   - 要求 `send_base ≤ peer.recv_offset ≤ send_end`，否则协议错误（无法重放）。
   - 发送游标 `cursor := peer.recv_offset`；释放 `< peer.recv_offset` 的重放缓冲。
   - 发送窗口上界 `:= peer.read_offset + peer.window`。
   - 若本端 `FIN_SENT` 且对端未置 `FIN_RECEIVED`，重放完数据后重发 `FIN`。
2. 对端没有该 stream：
   - **客户端**：重发 `OPEN`，`cursor := 0`（对端从未见过这个流）。
   - **服务端**：该流已被客户端遗弃，本地 RESET 并关闭。

对端 states 中有本端不认识的 stream，按角色处理（**客户端是流存在性的权威**）：

- **服务端**：忽略，等待客户端重放 `OPEN`（该流的 OPEN 随上一条 Link 一起丢了）。
- **客户端**：回 `RESET`，告诉服务端这个流已经被客户端retire。

## 4. 数据与流控

- `DATA.offset` 是该 Stream 的**绝对**发送偏移量。接收端：
  - `offset > recv_offset` → 协议错误（有序 Link + 精确重放使空洞不可能出现）；
  - `offset + len ≤ recv_offset` → 纯重复，丢弃；
  - 部分重叠 → 裁掉前缀，接收剩余部分。
  因此**不需要序列号去重表**，偏移量本身就是幂等键。
- 发送端在 `peer.read_offset + peer.window` 处阻塞；`window` 在 HELLO/HELLO_ACK 中各自声明
  （默认 1 MiB）。接收缓冲区容量恒等于本端 `window`，因此 Link 读循环**永不阻塞**，
  不会出现一个逻辑流卡死整条 Link。
- `ACK` 发送时机：`recv_offset` 前进时（与其他帧合并，最多每 5ms 一次）；
  或 `read_offset` 相对上次已 ACK 值前进超过 `window/4` 时立即发送。

## 5. 关闭语义

- `FIN{stream_id, final_offset}`：发送方不再发数据。接收方在 `recv_offset == final_offset`
  且缓冲区被读空后向应用返回 `io.EOF`。半关闭成立（对应 TCP 的 `shutdown(SHUT_WR)`，
  这是 `scp`/`ssh -W` 正确工作的必要条件）。
- `RESET{stream_id, code}`：异常终止，双向立即失败。
- `CLOSE{code, reason}`：Session 级终止，**不可恢复**。客户端收到后停止重连。
  Link 的纯粹消失（QUIC 超时/网络错误）**不是** CLOSE，必须触发重连。
