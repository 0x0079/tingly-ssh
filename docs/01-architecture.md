# 01 · 架构

## 1. 分层与职责切分

职责必须严格分层，任何一层不得越界（这是本项目最重要的设计约束）：

| 层 | 负责 | 不负责 |
| --- | --- | --- |
| **SSH（不改动）** | 用户认证、端到端加密、channel 语义、pty/exec/forward | 漫游、重连 |
| **TCP (localhost)** | 把 SSH 接进 bridge（或用 ProxyCommand 走 stdio） | 跨网络 |
| **Session 层（本项目核心）** | `session_id`、逻辑流多路复用、字节偏移量、replay、去重、流控、跨 link 重连 | 加密、拥塞控制 |
| **QUIC (quic-go)** | 握手与加密、拥塞控制、丢包恢复、**连接迁移**（Connection ID）、path validation、NAT rebinding | 进程级/断网级恢复 |
| **UDP** | 包传输 | — |

一句话区分 QUIC 与 Session 层：
**QUIC 解决"活着的连接换了地址"；Session 层解决"连接死了又活过来"。**

> 注意 quic-go 的能力边界：被动 NAT rebinding 由它自动处理；**客户端主动换网（换本地 UDP socket）需要显式调用
> `Conn.AddPath`/`Path.Probe`/`Path.Switch`，v0 未接入**（列入 M3）。因此 v0 的换网路径是
> "链路死亡 → 会话层重连"，SSH 依然不断，只是多一次握手停顿。迁移在本架构里是**优化，不是正确性依赖**。
> 详见 `adr/0003-library-choices.md` §2.1。

## 2. 组件图

```
┌──────────────────────── 客户端主机 ────────────────────────┐
│  ssh ──TCP──▶ listener ─┐                                 │
│  ssh -W / ProxyCommand ─┤                                 │
│       (stdio)           ▼                                 │
│                   ┌───────────┐   Stream（逻辑流，有序字节）│
│                   │  Session  │◀──────────────┐            │
│                   │  (client) │               │            │
│                   └─────┬─────┘        ┌──────┴──────┐     │
│                         │  Attach/Detach│  Supervisor │     │
│                         ▼               │ 重连+退避    │     │
│                   ┌───────────┐         └──────┬──────┘     │
│                   │   Link    │ 一条 QUIC 流   │            │
│                   └─────┬─────┘◀───────────────┘            │
└─────────────────────────┼───────────────────────────────────┘
                          │ QUIC / UDP（可迁移）
┌─────────────────────────┼───────────────────────────────────┐
│                   ┌─────▼─────┐                             │
│                   │   Link    │                             │
│                   └─────┬─────┘                             │
│                   ┌─────▼─────┐   Registry: session_id→Session│
│                   │  Session  │  （断链后保留 linger 时间）   │
│                   │  (server) │                             │
│                   └─────┬─────┘                             │
│                    每个逻辑流 ──TCP──▶ sshd:22               │
└──────────────────────── 服务端主机 ──────────────────────────┘
```

## 3. 关键概念

- **Session**：一个长生命周期的逻辑连接，由 16 字节随机 `SessionID` 标识。
  它可以在**零到多条** Link 上依次存活。Session 的状态（每个 Stream 的发送重放缓冲区、
  接收缓冲区、偏移量）与 Link 无关。
- **Link**：一次物理传输实例 = 一条 QUIC 连接上的一条双向 QUIC 流 + 帧编解码器。
  Link 死亡（QUIC 超时、迁移失败、进程对端重启）不杀死 Session。
  每条 Link 有单调递增的 `epoch`，用于拒绝过期 Link 的抢占。
- **Stream**：Session 内的逻辑流，对应一个 SSH TCP 连接。以 `uint64` ID 标识，
  客户端发起的为奇数（v0 只有客户端发起）。每个 Stream 是**绝对字节偏移量**语义的有序流。
- **Supervisor**：客户端侧的重连循环（指数退避 + jitter），负责 Dial → Handshake → Attach。

### 为什么一条 QUIC 流承载所有逻辑流？

替代方案是"一个逻辑流 = 一条 QUIC 流"。我们选择单条 QUIC 流 + 自研多路复用，原因：

1. 重连时需要**确定性的重放顺序**；多条 QUIC 流在新连接上重建会引入映射和竞态。
2. 重放语义只需要实现一次（在帧层），而不是每条流一套。
3. 代价是所有逻辑流共享一条有序流 → 有队头阻塞风险。
   我们用**每流独立的接收窗口 + 永不阻塞 Link 读循环**的流控消除了"一个慢流卡死全链路"的问题
   （详见 `03-session-resumption.md` §4）。真正残留的只有帧级排队，对 SSH 交互流量可忽略。

## 4. 进程内并发模型

每个 Session：

- `readLoop`（属于当前 Link）：解析帧 → 分派。**绝不阻塞**：写接收缓冲区前已由流控保证有空间。
- `writeLoop`（属于当前 Link）：从一个统一的出站队列取帧写入 Link，负责 DATA 分片、ACK 合并。
- 每个 Stream：调用方 goroutine 在 `Write` 上阻塞（发送缓冲区满时），在 `Read` 上阻塞（无数据时）。
- `keepalive`：周期 PING，检测半死链路（QUIC 自身也有 keepalive，这里是应用级探活）。
- 服务端 `reaper`：清理超过 `linger` 仍未重连的 Session。

Link 切换时：旧 `readLoop`/`writeLoop` 退出，Session 状态保留，新 Link 的两个循环启动。
Stream 的 `Read`/`Write` **不返回错误**，只是阻塞等待——这正是"SSH 感觉不到断网"的根因。

## 5. 三种运行模式

```
# 服务端（和 sshd 同机）
tingly-shell server --listen :7443 --target 127.0.0.1:22 --token-file /etc/tingly/token

# 客户端 A：本地端口转发模式
tingly-shell client --server host:7443 --listen 127.0.0.1:2222 --pin sha256:... --token-file ~/.tingly/token
ssh -p 2222 user@127.0.0.1

# 客户端 B：ProxyCommand 模式（推荐，无本地监听端口）
ssh -o ProxyCommand='tingly-shell proxy --server host:7443 --pin sha256:... --token-file ~/.tingly/token' user@host
```

## 6. v0 已知限制（写入 roadmap，不在代码里埋 TODO）

| 限制 | 影响 | 计划 |
| --- | --- | --- |
| 会话状态在内存 | 进程重启后会话丢失 | M4：状态快照 / socket handoff |
| 单条 QUIC 流 | 帧级排队 | M5：评估多流 + 分流重放 |
| 服务端目标白名单静态 | 不能按用户路由 | M3 |
| token 为 bearer（TLS 内） | 依赖 TLS 与 pin 的正确性 | M3：HMAC 挑战-响应 |
| 无多路径聚合 | 不提速，只续命 | 观望 IETF multipath QUIC |
