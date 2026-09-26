# 复用 SSH 密钥做隧道接入认证（ssh-key auth + TOFU）

- 状态：**已实现**（本设计与代码同一批提交）
- 日期：2026-09-24
- 关系：在 [ADR-0004](../docs/adr/0004-client-identity.md) 之上新增一种客户端身份；
  取代其中"方案 B（mTLS）"想要的那一条收益——**凭据不上线**。
- 相关风险：`docs/04-security-model.md` R-7（`--insecure`/无 pin 时 token 可被窃取）

## 0. 问题

用户从直连 SSH 切到 tingly-ssh，要配置什么？

```
 ┌──────────────── 你的电脑 ────────────────┐                ┌──────────────── 服务器 ────────────────┐
 │  ssh ──stdio──▶ tingly-ssh proxy ══════╪═══ QUIC/TLS ═══╪══▶ tingly-ssh server ──TCP──▶ sshd   │
 │   │                   │                  │                │           │                    │      │
 │  [A] ~/.ssh/id_*      │                  │                │           │   [A] authorized_keys    │
 │      known_hosts     [B] token + pin     │                │   [B] credentials（哈希）             │
 └──────────────────────────────────────────┘                └────────────────────────────────────────┘

 [A] SSH 层：端到端，迁移时不用改            ← 用户已经有
 [B] 隧道接入层：tingly 新加的一层           ← 迁移成本全在这里
```

[B] 是一份**新的秘密**（token），每台设备要 keygen → 分发 → 0600 保管 → 服务端粘哈希 → SIGHUP，
再加一个 pin。对"一个人、几台机器"的主流用户，这就是"切过来很麻烦"的全部来源。

## 1. 目标 / 非目标

目标：

1. **零新秘密**：客户端用 ssh-agent（或本地未加密私钥）里**已经有的** SSH 密钥证明身份。
2. **零 pin 配置**：在不发送 token 的前提下，服务端公钥走 TOFU（首次信任，像 `known_hosts`）。
3. 最简客户端配置收敛为一行：
   `ProxyCommand tingly-ssh proxy --server %h:7443`
4. 安全性**不低于**现有 token + pin，且在"连到假服务端"这一条上**严格更强**。
5. 保留 token 模式（CI / 无 agent 环境），两种方式可以在同一台服务端并存。

非目标：

- 服务端不读取任何用户的 `~/.ssh/authorized_keys`，不读 `/etc/ssh/ssh_host_*_key`。
- 不做 KRL（证书吊销列表）；证书撤销靠删 CA 行或到期。
- 不让隧道层知道 SSH 用户名（它在 SSH 密文里，隧道本来就看不到）。

## 2. 协议

### 2.1 握手流程

```
  用户设备                                                   服务端
  ┌────────────────────────┐                                ┌──────────────────────────────┐
  │ tingly-ssh proxy     │ ①── TLS 1.3 握手 ────────────▶ │                              │
  │                        │ ◀─────────────────────────────  │                              │
  │                        │    双方各自算出：                │                              │
  │                        │    E = TLS exporter             │                              │
  │                        │   （每条连接唯一，32 字节）       │                              │
  │        │ ② 签 SSHSIG(M)│                                │                              │
  │        ▼               │                                │                              │
  │  ssh-agent（私钥不出）  │                                │                              │
  │        │ 签名 S         │                                │                              │
  │        ▼               │                                │                              │
  │                        │ ③── HELLO{KEY_AUTH, 公钥, S} ─▶ │ ④ 公钥在 --authorized-keys？  │
  │                        │                                │   S 验证 SSHSIG(M') 通过？    │
  │                        │                                │   （M' 用服务端自己的 E 计算） │
  │                        │ ◀── HELLO_ACK{OK, ticket} ────  │   通过 → 身份 = ssh:<指纹>     │
  └────────────────────────┘                                └──────────────────────────────┘
```

- `E = ExportKeyingMaterial("EXPORTER-tingly-shell-hello", nil, 32)`（RFC 8446 §7.5）
- `M = "tingly-shell hello v1" || 0x00 || E || session_id(16B)`
- 签名对象不是 `M` 本身，而是 OpenSSH **SSHSIG** 结构（`ssh-keygen -Y sign` 同款）：

```
"SSHSIG" || string("tingly-shell-hello-v1") || string("") || string("sha512") || string(SHA-512(M))
```

### 2.2 线格式变更（向后兼容）

| 帧 | 变化 |
| --- | --- |
| `HELLO` | 新 flag bit1 `KEY_AUTH`。置位时，`states` 之后追加 `ticket(bytes) n(v) proof×n`，`proof := public_key(bytes) signature(bytes)`（均为 SSH wire 格式）。`n ≤ 8` |
| `HELLO_ACK` | 可选尾字段 `ticket(bytes)`。**只回给 `KEY_AUTH` 的 HELLO**，所以旧客户端永远收不到 |

兼容矩阵：

| | 旧服务端 | 新服务端 |
| --- | --- | --- |
| 旧客户端（token） | ✓ | ✓（线格式不变） |
| 新客户端 + token | ✓（不置 `KEY_AUTH`，线格式不变） | ✓ |
| 新客户端 + ssh key | ✗ HELLO 解析失败（尾部多余字节），客户端重试到 linger 超时 | ✓ |

最后一格是唯一的不兼容：**要用 ssh key，服务端先升级**。`PROTOCOL_VERSION` 不 bump，
因为 token 路径没有任何变化，bump 反而会把旧客户端全部拒掉。

### 2.3 中间人为什么拿到签名也没用

```
  客户端 ════TLS 连接 1════ 中间人 ════TLS 连接 2════ 真服务端
          exporter = E1              exporter = E2

  客户端签的是 E1 ──▶ 中间人拿到 sig(E1) ──▶ 转发给真服务端
                                              │
                                              ▼
                                    真服务端要的是 sig(E2)，E1 ≠ E2 → 拒绝
```

exporter 由 TLS 主密钥派生，中间人无法让两段连接的 E 相等（这就是 channel binding）。
因此：不需要自研 nonce、不需要重放窗口、不多一次往返——这也是它和 ADR-0004 §2
否决的"自研 HMAC 挑战-响应"的本质区别。

### 2.4 跨协议签名隔离

`SSHSIG` 以魔数 `"SSHSIG"` 开头，并带 namespace。SSH 登录签名的被签数据以
`uint32 len(session_id)` 开头，`"SSHS"` 作为长度是 0x53534853（约 1.4 GB），不可能合法，
所以**我们要来的签名不可能被挪用为 SSH 登录签名**，反之亦然；namespace 又把它和
`git`/`file` 等其他 SSHSIG 用途隔开。绝不对裸数据签名。

## 3. 恢复凭证（ticket）：换网不重签

```
  时间 ──────────────────────────────────────────────────────────────▶
  首次建链          断            重连               断          重连
     │              ✂              │                 ✂            │
  agent 签名 S                出示 ticket T                   出示 T
  （sk 钥匙要碰一下）         （不调用 agent）                （不调用 agent）
     └──▶ ACK 下发 T
```

- `T` = 32 字节 CSPRNG；服务端只存 `SHA-256(T)` 与签发时的公钥，**挂在会话上，随会话消亡**。
- 每次**签名**认证成功都换发新的 T（旧的作废）；ticket 认证的重连不换发。
- ticket 只能用于 `RESUME` 一个**已存在**的会话；不能新建会话，不能接管别的会话。
- **撤销语义不变**：用 ticket 重连时，服务端仍按当前 `--authorized-keys` 重新检查那把公钥
  （是否仍在、是否过期）。删行 + SIGHUP 后，该会话的下一次重连被拒。
- 客户端只在**本次连到的服务端公钥与签发 T 的那次相同**时才出示 T，否则改为重新签名
  （签名在哪里出示都安全，T 不是）。

## 4. 服务端信任（TOFU）

```
  第一次：client ──▶ server 出示证书 ──▶ known_servers 无记录 ──▶ 记录（并告警）
  之后：  client ──▶ server 出示证书 ──▶ 与记录一致？ ──否──▶ 拒绝，告诉用户删哪一行
```

- 文件：`~/.config/tingly-ssh/known_servers`（`--known-servers` 覆盖），每行 `host:port sha256:<pin>`。
- 服务端信任的决策表：

| 客户端配置 | 服务端认证方式 |
| --- | --- |
| `--pin` | pin（与今天相同） |
| `--insecure` | 不验证（仅开发，告警） |
| `--token-file`，无 pin | 系统 CA（与今天相同）——**token 不走 TOFU**，否则首连会把 token 送给中间人 |
| ssh key，无 pin | **TOFU** |

TOFU 在 ssh key 模式下是安全的：首次连接即使撞上中间人，对方拿不到任何可复用的东西（§2.3），
而里层 SSH 的 `known_hosts` 仍然端到端兜底。

## 5. 服务端白名单 `--authorized-keys`

格式照抄 OpenSSH `authorized_keys`，管理员可以直接复制用户的公钥行：

```
ssh-ed25519 AAAA... alice@laptop
expiry-time="20271231" ssh-ed25519 AAAA... ci-runner
cert-authority,principals="alice,bob" ssh-ed25519 AAAA... corp-user-ca
```

| 选项 | 行为 |
| --- | --- |
| `expiry-time="YYYYMMDD[HHMM[SS]]"` | 到期后拒绝（对齐 OpenSSH 语义，UTC） |
| `cert-authority` | 该行是 CA：接受由它签发的**用户证书** |
| `principals="a,b"` | 仅与 `cert-authority` 同用：证书主体须与之相交 |
| `from=...` | **拒绝整份文件**：我们会被误以为在执行它，实际 sshd 也看不到源地址（R-1） |
| `command=`/`no-pty`/`restrict`/`permitopen` 等 | 忽略：这些是 SSH 会话语义，sshd 自己执行 |

证书：校验签发 CA、类型为 user、有效期、principals；critical option 只接受 `force-command`
（sshd 执行）与 `source-address`（我们按客户端 UDP 源地址执行）。

身份：`ssh:<SHA256 指纹>`（证书取其内含公钥的指纹）；日志 label 用注释或证书 KeyId。
会话绑定、每身份配额、审计归因全部沿用 ADR-0004 的机制。

算法：拒绝 `ssh-dss` 与 SHA-1 的 `ssh-rsa` 签名；RSA 必须是 `rsa-sha2-256/512`。
`sk-*`（FIDO）按 OpenSSH 规则要求 user-presence 标志。

## 6. 客户端选钥

- 默认：ssh-agent（`SSH_AUTH_SOCK`）里的所有非 `sk-` 密钥，每把签一次，一起放进 HELLO（上限 8）；
  服务端接受第一把命中的。这与 ssh 自己向 sshd 逐个出示公钥的暴露面相同。
- `sk-`（FIDO）密钥默认不用：每签一次都要碰一下，只有 `--identity` 明确指定时才用。
- `--identity PATH`：未加密私钥直接读；加密私钥或 `.pub` → 在 agent 里找对应那把。
- 启动时就检查"至少有一把可用的钥匙"，否则直接报错退出，而不是重试到 linger 超时。

## 7. 安全边界（不做的事）

```
  ✗ 服务端读 /home/*/.ssh/authorized_keys     → 需要高权限，且它不知道 SSH 用户名
  ✗ 服务端复用 /etc/ssh/ssh_host_*_key        → 需要 root，且一把钥匙跨两个协议
  ✗ 让 agent 对裸数据签名                      → 可能被挪用为 SSH 登录签名
  ✓ SSHSIG + namespace "tingly-shell-hello-v1"
  ✓ 独立白名单文件，格式照抄 authorized_keys，支持 cert-authority
  ✓ token 模式保留，给 CI / 无 agent 环境
```

## 8. 实现落点

| 包 | 内容 |
| --- | --- |
| `internal/proto` | `FlagKeyAuth`、`Hello.Ticket/Proofs`、`HelloAck.Ticket`、编解码与上限 |
| `internal/auth/sshsig.go` | `HelloMessage`、`SignedData`（SSHSIG）、签名算法白名单 |
| `internal/auth/keys.go` | `KeyStore`：解析 authorized_keys、`Authorize`、`Verify`、`Reload` |
| `internal/auth/signer.go` | 客户端 `Prover`：agent / 本地私钥 → proofs |
| `internal/transport` | `Link.Exporter()`、`Link.PeerPin()`、`KnownServers` 与 TOFU 的 `ClientTLS` |
| `internal/bridge` | HELLO 构造、ticket 保存与出示、服务端 `authenticate`、ticket 表 |
| `cmd/tingly-ssh` | `server --authorized-keys`、`client/proxy --identity --known-servers`，token 变为可选 |

## 9. 测试与 harness

单元（`go test -race ./...`）：

| 层 | 用例 |
| --- | --- |
| proto | KEY_AUTH HELLO 往返；无 flag 时线格式与旧版逐字节相同；ACK ticket 可选；proof 数量上限 |
| auth/sshsig | 签名/验签；错 namespace、错消息、裸签名、SHA-1 RSA 被拒 |
| auth/keys | 解析（注释、选项、坏行、`from=` 拒绝）；expiry；cert-authority + principals + source-address；Reload 失败保留旧集合 |
| auth/signer | 真 agent（`agent.NewKeyring` + unix socket）列钥、签名；`--identity` 选择；sk 默认排除 |
| transport | exporter 两端一致、不同连接不同；TOFU 首次记录、二次匹配、换钥拒绝 |
| bridge | key 认证成功；未授权的钥被拒且不重试；**中继签名被拒**（签名绑定到别的 exporter）；ticket 重连不再调用签名器；撤销后 ticket 重连被拒；ticket 不能接管别的会话；token 与 key 并存 |

端到端（`test/e2e/run.sh`，真 ssh / sshd / ssh-agent）：

| 编号 | 场景 |
| --- | --- |
| E15 | 只有 ssh-agent + `--authorized-keys`：`ProxyCommand tingly-ssh proxy --server …`，**无 token、无 pin**，ssh 登录成功，known_servers 被写入 |
| E16 | 同一 key 会话被 `SIGUSR1` 断链两次，数据不丢，客户端日志显示 ticket 恢复（不重签） |
| E17 | 未在白名单里的 key 被拒，客户端立即退出不重试 |
| E18 | 服务端换钥后 TOFU 拒绝连接并给出 known_servers 行号提示 |
