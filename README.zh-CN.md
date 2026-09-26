<div align="center">

# tingly-ssh

**你的 SSH，不会再断了。**
Wi-Fi 切 5G、合上盖子、地铁进隧道，还是原来那条 `ssh` 会话，接着往下跑，一行输出都不丢。

[English](README.md) | 简体中文 · [项目主页](https://0x0079.github.io/tingly-ssh/)

[![CI](https://github.com/0x0079/tingly-ssh/actions/workflows/ci.yml/badge.svg)](https://github.com/0x0079/tingly-ssh/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/0x0079/tingly-ssh)](https://github.com/0x0079/tingly-ssh/releases)
[![License: MPL-2.0](https://img.shields.io/badge/license-MPL--2.0-blue.svg)](LICENSE)

<img src="docs/assets/demo.gif" alt="两条 ssh 上跑同一个长任务：笔记本 IP 一变，普通 ssh 的进度条停在 15% 然后超时；走 tingly-ssh 的进度条扛过 IP 变化和 15 秒断网，一直跑到 100%" width="820">

<sub>真录的，不是动画：一个真实的"笔记本"网络命名空间先换 IP，再彻底断网 15 秒。
两条 ssh 上跑同一个长任务。左边是普通 <code>ssh</code>，进度条停在 15%，然后直接放弃；右边走 tingly-ssh，断网时进度条停住，恢复后追上进度，一直跑完。
录制脚本会检查 150 次进度更新是否全部按顺序、不重不漏地到达了终端。用 <a href="demo/record.sh"><code>demo/record.sh</code></a> 可以自己复现。</sub>

</div>

---

`ssh`、`scp`、`rsync`、`git`、`-L/-R/-D` 转发、`ProxyJump`、tmux……你今天怎么用，明天还怎么用。
**不改 OpenSSH，不改 sshd。**两端各放一个小 bridge，中间用 QUIC 加一个可恢复的会话层来承载连接。
地址变了、短时断网了，它会自己恢复，而不是甩给你一句 `client_loop: send disconnect: Broken pipe`。

## 适合你吗？

**适合，如果你：**

- 用笔记本在家、公司、咖啡店、手机热点之间来回切；
- 常通过 SSH 跑长编译、数据迁移、训练任务，受够了守着连接；
- 开着 `ssh -L` 隧道连数据库、Jupyter、内网面板，Wi-Fi 一断它们就一起断；
- 经常在高铁、飞机、信号不稳的移动网络上，网络时不时消失 10 到 60 秒；
- 想要上面这些，但不想上 VPN、托管控制面或者新的身份系统。

**可能不适合，如果你：**

- 需要会话扛过**任意一端机器重启**：会话状态在内存里（见[已知限制](#现状与已知限制)）；
- 用 **Windows**：目前只支持 Linux 和 macOS；
- 所在网络**封 UDP**（部分公司网络）：QUIC 需要一个 UDP 端口，TCP 兜底在[路线图](docs/05-roadmap.md)里；
- 主要在**极高延迟**的链路上敲命令：这时 [mosh](https://mosh.org) 的本地回显预测更合适。

## 和其他方案比

|  | **tingly-ssh** | mosh | Eternal Terminal | autossh | 只用 tmux / screen |
| --- | --- | --- | --- | --- | --- |
| IP 变了还能用 | ✅ | ✅ | ✅ | ❌ 重连成新会话 | ❌ 需要手动 reattach |
| 短时断网能恢复 | ✅ 默认 60 秒，可调 | ✅ | ✅ | ❌ 新会话 | ❌ 需要手动 reattach |
| 输出**逐字节**送达 | ✅ | ❌ 只同步最新一屏 | ✅ | ❌ | ✅ 在 pane 里，连接本身不行 |
| `scp`、`sftp`、`rsync`、SSH 上的 `git` | ✅ | ❌ 只有终端 | ❌ 终端和隧道 | ✅ | ✅ |
| `-L` / `-R` / `-D` 转发、agent 转发 | ✅ | ❌ | 部分 | ✅ | ✅ |
| 终端回滚（scrollback）正常 | ✅ | ❌ | ✅ | ✅ | ✅ |
| sshd 不改，SSH 认证与加密不变 | ✅ | ✅ 用 ssh 引导 | ✅ | ✅ | ✅ |
| 服务端需要 | 一个进程，一个 UDP 端口 | `mosh-server`，UDP 60000–61000 | `etserver` 守护进程 | 无 | 无 |
| 慢链路上的输入预测 | ❌ | ✅ | ❌ | ❌ | ❌ |

tingly-ssh 和 tmux 分工不同，配合使用最好：隧道负责让**连接**扛过网络变化；万一连接真的断了
（比如笔记本关机一小时），由 tmux 保住**正在做的事**。

## 快速上手

大约五分钟。需要：Linux / macOS 客户端，跑着 sshd 的 Linux / macOS 服务器，以及一个能放行的 UDP 端口。

### 1. 两端都装上

每个 [release](https://github.com/0x0079/tingly-ssh/releases) 都附带 linux/darwin × amd64/arm64 的预编译包：

```bash
# 把 VERSION、OS（linux|darwin）、ARCH（amd64|arm64）换成实际值
curl -LO https://github.com/0x0079/tingly-ssh/releases/download/vVERSION/tingly-ssh_VERSION_OS_ARCH.tar.gz
tar xzf tingly-ssh_VERSION_OS_ARCH.tar.gz
sudo install -m 755 tingly-ssh /usr/local/bin/
tingly-ssh version
```

也可以用 Go 1.26+：`go install github.com/0x0079/tingly-ssh/cmd/tingly-ssh@latest`。

### 2. 服务端：和 sshd 放在一起

隧道直接复用你已有的 SSH 密钥，白名单就是一个普通的 `authorized_keys` 文件：

```bash
sudo mkdir -p /etc/tingly
sudo sh -c 'cat ~alice/.ssh/authorized_keys >> /etc/tingly/authorized_keys'

tingly-ssh server --listen :7443 --target 127.0.0.1:22 \
    --authorized-keys /etc/tingly/authorized_keys
# ... msg="server listening" addr=[::]:7443 pin=sha256:XUYr8w...   <- 记下这个 pin
```

在防火墙或云安全组里放行 **UDP** 7443。systemd unit 见[迁移指南](docs/09-migrating-from-ssh.md#23-启动systemd)。

### 3. 客户端：`~/.ssh/config` 里加一行

在原来的 Host 旁边新加一个别名，两个可以对比着用，随时能退回：

```sshconfig
Host myserver-roam
    HostName 203.0.113.10
    User alice
    ProxyCommand tingly-ssh proxy --server %h:7443
```

确认钥匙在 agent 里（`ssh-add -l`），然后连接：

```console
$ ssh myserver-roam
level=WARN msg="trusting this server key from now on" pin="sha256:XUYr8w..."
alice@myserver:~$
```

这行只会出现一次，作用和 SSH 的 "Are you sure you want to continue connecting" 一样：拿它和服务端打印的 pin 核对一下。

### 4. 亲眼看看

```bash
ssh myserver-roam 'while true; do date; sleep 1; done'
```

然后关掉再打开 Wi-Fi，或者切到手机热点，或者合上盖子半分钟。时钟会停一下，再把错过的每一秒补上。
不用重新登录，也不用重跑命令。

用着满意，就把 `ProxyCommand` 这一行挪进你平时用的 `Host`；想退回，删掉这一行就行。
团队场景、SSH CA、硬件钥匙和排错表都在完整的[从直连 SSH 迁移](docs/09-migrating-from-ssh.md)里。

## 工作原理

```
               你的笔记本                                         你的服务器
┌──────────────────────────────────┐                ┌──────────────────────────────────┐
│ ssh ─stdio─▶ tingly-ssh proxy  │═══ QUIC/UDP ══▶│ tingly-ssh server ─TCP─▶ sshd  │
└──────────────────────────────────┘                └──────────────────────────────────┘
                   └──────────────── 可恢复会话层 ────────────────┘
```

连接会以两种方式断掉，各有一套机制对付：

- **地址变了，但包还能通**（Wi-Fi 切 5G、NAT 重绑定）：交给 QUIC 的连接迁移，通常你根本察觉不到。
- **链路断了一阵**（隧道、合盖、热点掉线）：会话层把发出的每个字节都留着，直到对端确认收到。新链路建立后，两端先对一下各自收到了哪儿，再把缺的部分重放，SSH 字节流既看不到缺口，也看不到重复。

认证和端到端加密仍然由 SSH 自己负责，隧道看不到 SSH 流里的内容。
细节见：[架构](docs/01-architecture.md)、[恢复语义](docs/03-session-resumption.md)、[为什么在 QUIC 之上还要一层会话层](docs/adr/0002-resumable-session-layer.md)。

## 安全性，一段话说清

tingly-ssh 是套在 SSH 外面的**额外**一层，不是替代：sshd 照样认证你，SSH 照样端到端加密一切。
想通过隧道，客户端必须证明自己持有服务端白名单里的某把钥匙。这个证明是一个 SSH 签名，绑定在当前这一条 TLS 连接上，
既不能被重放，也不能被挪用成 SSH 登录签名。线上不传任何秘密，所以隧道服务端的公钥可以像 `known_hosts` 那样首次信任。
撤销权限就是删掉一行，再发一个 `SIGHUP`。
保护了什么、隧道会暴露什么、风险清单和加固检查表，都写在[安全模型](docs/04-security-model.md)里。

## 更多用法

<details>
<summary><b>每设备 token</b>：CI、没有 ssh-agent 的机器</summary>

```bash
tingly-ssh keygen --label ci-runner-1 > token && chmod 600 token   # 服务端记录行打印在 stderr
tingly-ssh server --listen :7443 --target 127.0.0.1:22 --credentials /etc/tingly/credentials
ssh -o ProxyCommand="tingly-ssh proxy --server %h:7443 --token-file token --pin sha256:..." user@host
```

服务端只存哈希。每台设备一份 token，撤销某一台就是删掉它那一行再发 `SIGHUP`。两种认证方式可以在同一台服务端上同时开启。
为什么 `--pin` 和 `--token-file` 两个都要：见[安全模型 §2.1](docs/04-security-model.md)。
</details>

<details>
<summary><b>跳板机</b>（笔记本 → 跳板机 → 内网机器）</summary>

只给第一跳加隧道，后面照常用原生的 `ProxyJump`。笔记本换网时断的只有第一跳，所以跳板机上什么都不用改。
见[跳板机拓扑](docs/08-jump-host-topologies.md)。
</details>

<details>
<summary><b>本地端口</b>代替 ProxyCommand</summary>

```bash
tingly-ssh client --server myserver:7443 --listen 127.0.0.1:2222
ssh -p 2222 alice@127.0.0.1
```
</details>

<details>
<summary><b>调参</b></summary>

| 参数 | 位置 | 作用 |
| --- | --- | --- |
| `--session-linger` | 客户端和服务端 | 断网多久以内还能恢复（默认 60s） |
| `--idle-timeout`、`--keepalive` | 客户端 | 多快发现链路已死；笔记本用 `8s` / `2s` 比较合适 |
| `--window` | 两端 | 每条流的窗口，同时限制重放缓冲占用的内存 |
| `--max-sessions` | 服务端 | 限制服务端内存。启动时会打印最坏情况，请按实际并发来设 |

对客户端或 proxy 进程发 `kill -USR1` 会立即重连，适合挂在网络切换的钩子里。
</details>

## 常见问题

**断网时间超过 linger 会怎样？**
会话会被放弃，`ssh` 正常退出，和现在一样。断网经常更久的话，在两端都调大 `--session-linger`，再用 tmux 兜底。

**会让 SSH 变快吗？**
不会，这不是它的目标，它的目标是不断。性能相关的工作在[路线图](docs/05-roadmap.md) M5 里。

**跑隧道的人能看到我的会话内容吗？**
看不到。它转发的是 SSH 协议本身，在你的 `ssh` 和 sshd 之间早就端到端加密了。

**sshd 看到的连接都来自 127.0.0.1，有影响吗？**
有：sshd 上的 `from="..."` 限制，以及 fail2ban 这类按 IP 工作的工具，都会受影响。隧道服务端的日志会记下每个会话属于哪把钥匙或哪台设备。见[安全模型 R-1](docs/04-security-model.md)。

**为什么用 QUIC，而不是 MPTCP 或 WireGuard？**
MPTCP 需要两端内核都支持，不支持时会静默退化；WireGuard 能漫游，但里面的 TCP 连接遇到长时间断网照样会死。完整理由见 [ADR-0001](docs/adr/0001-transport-choice.md)。

## 我们怎么知道它可靠

每次 push 都会对着**真实的 sshd** 和**真实的 OpenSSH 客户端**跑：

- **18 个端到端用例**：ProxyCommand、scp 完整性、销毁链路、30 秒网络黑洞、linger 截止、凭据被拒、经由未改动跳板机的两跳链路（[`test/e2e`](test/e2e/)）；
- **17 个日常 SSH 场景**：交互式 pty、32 MiB 输出、全部 256 个字节值、Ctrl-C、窗口缩放、`-L/-R/-D`、sftp、rsync、ControlMaster、tmux（[`test/scenarios`](test/scenarios/)）；
- **7 个漫游用例**，用远端计数器测量：计数连续说明没丢也没重，最大到达间隔就是终端实际卡住的时长。同一套用例也能通过 `nmcli` 或 macOS 驱动真实的无线网卡（[`test/roaming`](test/roaming/)）；
- 会话层的单元测试和故障注入测试，在 `go test -race` 下运行。

完整的验收清单见[验证计划](docs/07-verification-plan.md)。

## 现状与已知限制

v0：设计、会话层、QUIC 承载、bridge、CLI 和各套测试都已完成（里程碑 M0 到 M2）。
已知限制都在[路线图](docs/05-roadmap.md)里跟踪：

- 客户端或服务端进程重启后，会话不能恢复；
- 只走 UDP，TCP 兜底已在计划中；
- 只支持 Linux 和 macOS；
- 客户端主动发起的路径迁移还没用上 quic-go 的 Path API（目前由重连覆盖）。

## 文档

设计文档都在 [`docs/`](docs/README.md)：
[范围](docs/00-vision-and-scope.md) · [架构](docs/01-architecture.md) · [线协议](docs/02-wire-protocol.md) ·
[恢复语义](docs/03-session-resumption.md) · [安全模型](docs/04-security-model.md) · [路线图](docs/05-roadmap.md) ·
[测试](docs/06-testing.md) · [验证计划](docs/07-verification-plan.md) · [跳板机](docs/08-jump-host-topologies.md) ·
[从直连 SSH 迁移](docs/09-migrating-from-ssh.md) · ADR [0001](docs/adr/0001-transport-choice.md) [0002](docs/adr/0002-resumable-session-layer.md) [0003](docs/adr/0003-library-choices.md) [0004](docs/adr/0004-client-identity.md)

## 参与贡献

欢迎提 issue 和 PR。提交代码前请先跑：

```bash
gofmt -l . && go vet ./... && go test -race ./...
./test/e2e/run.sh && ./test/scenarios/run.sh    # 需要 OpenSSH；两者都不会碰你的 SSH 配置
```

项目只有两个直接依赖：[quic-go](https://github.com/quic-go/quic-go) 和 `golang.org/x/crypto`，
希望一直保持这样（[ADR-0003](docs/adr/0003-library-choices.md)）。项目主页的源码在 [`site/`](site/)。

## 许可证

[Mozilla Public License 2.0](LICENSE)
