# 08 · 跳板机（多跳）场景

日常用法是 `笔记本 → 跳板机 → 目标机`，**跳板机不可改动**，ssh 配置也只能加不能大改。

## 1. 结论先行

支持，**跳板机零改动**，ssh 侧只需在配置里新增一段 `Host` 块，用法仍是 `ssh prod-web01`。

但有一个硬前提：**必须有一台"跳板机这一侧、客户端能用 UDP 直达、且我们能装东西"的机器**
来跑 `tingly-shell server`。没有这样的机器就无解，见 §5。

## 2. 为什么只保护第一跳就够

换网断的是"一端在笔记本上"的那些连接。跳板机到目标机那一段在机房里，网络不变。

| 连接 | 笔记本换网时 | 谁保护 |
| --- | --- | --- |
| 笔记本 → 跳板机 (ssh, TCP) | **断** | ← 套进 tingly 隧道 |
| 跳板机 → 目标机 (ssh over direct-tcpip) | 不受影响 | 无需保护 |
| 目标机上的 shell / tmux | 不受影响 | 无需保护 |

把第一跳换成隧道之后，整条链路在换网、NAT rebinding、短时断网后继续存活，
目标机上的会话不重连、不重新认证。

## 3. 推荐拓扑：sidecar 跑 server，跳板机原样不动

```
                    ┌──────────────── 机房 / VPC ─────────────────┐
笔记本                │                                            │
  ssh ──ProxyCommand──┼──▶ tingly server (sidecar，我们可控)        │
        QUIC/UDP      │        │ TCP 127.0.0.1 或内网              │
                      │        ▼                                  │
                      │   跳板机 sshd:22  ← 完全没改               │
                      │        │ ssh 标准 direct-tcpip (ProxyJump) │
                      │        ▼                                  │
                      │   目标机 sshd:22  ← 完全没改               │
                      └───────────────────────────────────────────┘
```

sidecar 可以是同机房任意一台我们有权限的机器（一台最小规格的 VM 就够，它只转发字节）。
它唯一需要的能力是：客户端能用 UDP 连到它，它能用 TCP 连到跳板机的 22 端口。

服务端：

```bash
tingly-shell server --listen :7443 \
    --target jump.internal:22 \
    --token-file /etc/tingly/token
```

`--target` 是白名单，**只放跳板机一个地址**，sidecar 就不会变成任意 TCP 的开放中继。

客户端 `~/.ssh/config`（只新增，不改动已有条目）：

```sshconfig
Host jump
    HostName jump.example.com
    User me
    ProxyCommand tingly-shell proxy --server sidecar.example.com:7443 \
        --target jump.internal:22 --token-file ~/.tingly/token --pin sha256:...

Host prod-*
    User me
    ProxyJump jump
```

用法完全不变：

```bash
ssh prod-web01          # 笔记本→(隧道)→跳板机→prod-web01
scp file prod-web01:/tmp/
```

`ProxyJump` 是 OpenSSH 标准功能，靠的是跳板机 sshd 默认就开的 `direct-tcpip` 转发，
不需要在跳板机上装任何东西、改任何配置。

### 3.1 不想用 ProxyCommand 的写法

常驻一个客户端，把第一跳变成本地端口：

```bash
tingly-shell client --server sidecar.example.com:7443 \
    --target jump.internal:22 --listen 127.0.0.1:2222 \
    --token-file ~/.tingly/token --pin sha256:...
```

```sshconfig
Host jump
    HostName 127.0.0.1
    Port 2222
    User me

Host prod-*
    User me
    ProxyJump jump
```

两种写法的区别：`ProxyCommand` 每个 ssh 进程一条独立 session；常驻 client 让
所有 ssh 复用一条 session（多个逻辑流），换网时**一次重连恢复全部会话**，且可以用
`kill -USR1` 主动换路。多会话场景推荐常驻写法。

## 4. 三跳及以上

`ProxyJump` 支持链式，仍然只保护第一跳：

```sshconfig
Host prod-*
    ProxyJump jump,jump2      # jump 走隧道，jump2 及之后都是标准 ssh
```

## 5. 什么情况下做不到（诚实的边界）

**跳板机那一侧没有任何我们能装东西的机器，或者没有任何 UDP 入口。**
这时 QUIC 根本进不去，本方案无能为力。不要指望下面这两条路：

- **把 QUIC 从跳板机的 ssh 里穿过去**：`-L`/`-D`/`direct-tcpip` 只转发 TCP，不转发 UDP；
  `-w` 需要 root 和 `PermitTunnel`，那就是改跳板机了。
- **就算能转发 UDP**：承载它的那条 ssh 本身就是换网会断的那条，套进去等于没保护。

可行的替代，按代价从低到高：

1. 找网络/运维**放行一条 UDP 端口到一台我们可控的机器**。这是比"改跳板机"小得多的请求，
   通常也更容易通过审批。
2. 客户端与服务端都在自己掌控的内核上时，用 **MPTCP**（见 ADR-0001）。
3. **mosh**：需要在远端安装，且不透明转发（没有 scp、端口转发）。
4. 等 roadmap M5 的 **TCP 承载兜底**：会话层只要求 Link 是"可靠有序字节流"
   （`io.ReadWriteCloser`），所以在 UDP 被封、但 TCP 能直达某台可控机器时，
   可以换一层 TCP 承载。注意它救不了"只有跳板机可达"的情况，原因同上。

## 6. 安全影响

- sidecar 的 `--target` 白名单只放跳板机，隧道本身不放大可达面。
- 链路仍然经过跳板机，**审计与访问控制不被绕过**：跳板机看到的连接和以前完全一样。
- 不要为了省事把 `--target` 直接指向目标机来绕开跳板机——那会绕过审计，是策略问题不是技术问题。
- SSH 的端到端认证与加密不变：隧道看到的始终是 SSH 密文。

## 7. 验证

自动用例（`docs/07-verification-plan.md` §2）：

| ID | 覆盖 |
| --- | --- |
| E12 | `ssh -J` 经未改动的跳板机到达目标机，第一跳走隧道 |
| E13 | 两跳会话进行中，两次销毁第一跳链路，目标机上的输出不丢不断 |

测试环境用两个独立 sshd 实例（跳板机 + 目标机），都用脚本生成的独立配置启动，
系统 SSH 配置不动。
