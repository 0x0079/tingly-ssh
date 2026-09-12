# 03 · 会话恢复语义

本文档是实现的"正确性合同"。`internal/mux` 的测试按本文档逐条验证。

## 1. 三类故障与各层的分工

| 故障 | 现象 | 由哪一层解决 |
| --- | --- | --- |
| 换网（Wi-Fi→5G）、NAT rebinding | 源地址变了，但路径仍可达 | **QUIC**：Connection ID + path validation，连接不断，Session 层完全不知情 |
| 断网几十秒、迁移失败、对端网络栈重置 | QUIC 连接死亡（idle timeout / 不可达） | **Session 层**：新建 Link，HELLO 对齐偏移量并重放 |
| 进程重启、换设备 | 内存状态丢失 | v0 **不支持**（见 roadmap M4） |

## 2. 不变量（invariants）

对任意 Stream，记发送端 `base ≤ cursor ≤ end`，接收端 `read ≤ recv`：

- **I1 无丢失**：`[base, end)` 的字节一直保留在重放缓冲区，直到对端 ACK 的 `recv_offset` 超过它。
- **I2 无重复交付**：应用只会读到严格递增的偏移量区间；重叠的 DATA 在接收端被裁剪。
- **I3 无空洞**：交付给应用的字节偏移量连续。
- **I4 有界内存**：单 Stream 内存 ≤ `send_window + recv_window`（默认 2 MiB）。
- **I5 对应用透明**：Link 断开期间 `Stream.Read`/`Write` 只阻塞，不返回错误；
  只有 Session 终止（CLOSE / linger 超时 / 显式关闭）才返回错误。

## 3. 客户端重连循环

```
attempt := 0
for session 未终止:
    link, err := dial + handshake(epoch = ++epoch)
    if err:
        if err 是 UNAUTHORIZED / UNSUPPORTED_VERSION / SESSION_UNKNOWN: 终止 Session
        sleep(backoff(attempt++)); continue
    attempt = 0
    session.Attach(link)      // 阻塞直到该 Link 死亡
```

退避：`min(500ms × 2^attempt, 15s)` 再叠加 ±20% jitter。
`linger`（默认 60s，`--session-linger` 可调）内未成功重连 → 终止 Session，向所有 Stream 返回错误，
SSH 此时才会看到断连。服务端侧同样以 `linger` 回收未重连的 Session。

## 4. 为什么不会队头阻塞

所有逻辑流共享一条 QUIC 流。若接收端因为应用读得慢而阻塞 Link 读循环，其他逻辑流会一起饿死。
消除方式：

1. 每个 Stream 的接收缓冲区容量 == 本端声明的 `window`；
2. 发送端严格遵守 `peer.read_offset + peer.window` 上界；
3. 因此"缓冲区写不进去"在协议上不可能发生 → 读循环只做内存拷贝，**永不阻塞**；
4. 违反窗口的对端被判协议错误，Session 以 `CLOSE` 终止（不静默截断）。

残留的是帧级排队：一个大 DATA 帧（≤ 64 KiB 分片）会延后后面的帧若干微秒级时间。
对 SSH 交互流量可忽略；大文件传输场景见 roadmap M5。

## 5. 重放的内存代价

最坏情况：断网瞬间，发送窗口全满 → 每个 Stream 保留 1 MiB 待重放数据。
`--max-streams`（默认 64）× 2 MiB = 128 MiB 上界。默认窗口对 SSH 交互足够，
批量传输可用 `--window` 调大，同时按上式评估内存。

## 6. 明确的失败模式

| 场景 | 行为 |
| --- | --- |
| 服务端重启 | 客户端重连成功但 `SESSION_UNKNOWN` → Session 终止，SSH 断开（v0 预期行为） |
| 客户端重启 | Session 丢失；服务端侧 Session 在 linger 后被回收 |
| 旧 Link 延迟到达并尝试接管 | `EPOCH_STALE`，旧 Link 被拒绝，不影响新 Link |
| 对端发来超出窗口的数据 | 协议错误 → `CLOSE{code=PROTOCOL}`，不静默丢数据 |
| `recv_offset` 落在重放缓冲区之外 | 无法重放 → `CLOSE{code=PROTOCOL}`（宁可可见失败，不要悄悄丢字节） |
