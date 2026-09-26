# 09 · 从直连 SSH 切换到 tingly-ssh（用已有的 SSH 密钥）

这份文档写给**已经在用 SSH** 的人：`~/.ssh/config` 里有 Host，ssh-agent 里有钥匙，
服务器上 `authorized_keys` 早就配好了。目标是**不生成任何新秘密**，改一行配置就换上可漫游的连接，
而且随时能退回去。

原理与安全分析见 [`.design/ssh-key-auth.pencil.md`](../.design/ssh-key-auth.pencil.md)。
这里只讲怎么做。

## 0. 切换前后，什么变了，什么没变

```
之前：  ssh ────────────── TCP ─────────────────▶ sshd
之后：  ssh ─stdio─▶ tingly-ssh proxy ═QUIC═▶ tingly-ssh server ─TCP─▶ sshd
                          │                            │
                          └─ 用 ssh-agent 里的钥匙签名 ─┘   ← 唯一新增的一环
```

| | 切换后 |
| --- | --- |
| 你的 SSH 私钥、`~/.ssh/known_hosts`、服务器上的 `sshd_config` 与 `authorized_keys` | **不变** |
| `scp`、`sftp`、`rsync -e ssh`、`git@…`、`ssh -L/-R/-D`、`ProxyJump`、tmux | **不变**，它们都走 ssh，ssh 走隧道 |
| SSH 登录认证 | **不变**，仍然由 sshd 端到端完成，隧道看不到内容 |
| 新增：服务器上跑一个 `tingly-ssh server` | 一次性，管理员做 |
| 新增：`~/.ssh/config` 里多一行 `ProxyCommand` | 每台客户端一行 |
| 新增：要记住的秘密 | **没有** |

## 1. 典型用户场景

| 场景 | 本文怎么走 |
| --- | --- |
| **A. 个人**：一台笔记本 + 一两台自己的服务器，经常在 Wi-Fi / 热点 / 公司网之间切 | §2 + §3，十分钟 |
| **B. 团队**：多人共用若干服务器，公钥已经分散在各自的 `authorized_keys` 里 | §2.2 选"汇总公钥"；有 SSH CA 就用一行 `cert-authority` |
| **C. 跳板机**：`笔记本 → 跳板机 → 内网机器` | 只给第一跳加隧道，见 §3.4 |
| **D. 硬件钥匙 / 1Password / Secretive 之类的 agent** | 可以用，注意 §4 的两个坑 |
| **E. CI、脚本、没有 agent 的机器** | 不适合本文，用每设备 token（[README](../README.md) 的 token 小节） |

## 2. 服务端（每台服务器一次）

### 2.1 安装与放行端口

```bash
# 按 README 的 Install 一节安装到 /usr/local/bin/tingly-ssh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin tingly
sudo install -d -o root -g tingly -m 0750 /etc/tingly
# 防火墙放行一个 UDP 端口，下面都用 7443
sudo ufw allow 7443/udp        # 或者云厂商安全组里放行 UDP 7443
```

> 服务器已有 tingly-ssh 的话，**先升级服务端**：旧服务端不认识 SSH 密钥认证，
> 新客户端用钥匙连旧服务端，只会看到一串 `reconnect failed`。

### 2.2 建隧道的白名单

白名单文件就是 OpenSSH 的 `authorized_keys` 格式，**不需要**让用户做任何事，管理员复制现成的公钥即可。
隧道服务端**不会**去读任何人的 `~/.ssh`，所以要把公钥抄一份到它自己的文件里：

```bash
# 场景 A：就是你自己
sudo sh -c 'cat ~alice/.ssh/authorized_keys >> /etc/tingly/authorized_keys'

# 场景 B：把几位同事的公钥汇总进来（重复的行无害）
for u in alice bob carol; do sudo cat /home/$u/.ssh/authorized_keys; done \
    | sudo tee -a /etc/tingly/authorized_keys >/dev/null

# 场景 B'：组织已经有 SSH 用户证书 CA，那就只要一行
echo 'cert-authority,principals="alice,bob,carol" ssh-ed25519 AAAA...CA公钥... corp-user-ca' \
    | sudo tee -a /etc/tingly/authorized_keys >/dev/null

sudo chgrp tingly /etc/tingly/authorized_keys && sudo chmod 0640 /etc/tingly/authorized_keys
```

复制过来的行里常见的选项都可以原样保留：

| 选项 | 在隧道这一层 |
| --- | --- |
| `command=`、`no-pty`、`restrict`、`permitopen=`、`no-port-forwarding` 等 | 忽略。它们描述的是 SSH 会话，sshd 自己会执行 |
| `expiry-time="20271231"` | 生效，到期后隧道也拒绝 |
| `cert-authority`、`principals=` | 生效，见上 |
| `from="…"` | **整份文件拒绝加载**，请删掉该选项。原因是隧道没法替 sshd 执行它；接上隧道后 sshd 看到的来源地址也不再是用户的 IP（`04-security-model.md` R-1），所以这条限制会悄悄失效 |

### 2.3 启动（systemd）

```ini
# /etc/systemd/system/tingly-ssh.service
[Unit]
Description=tingly-ssh (SSH over QUIC)
After=network-online.target sshd.service

[Service]
User=tingly
StateDirectory=tingly-ssh
ExecStart=/usr/local/bin/tingly-ssh server \
    --listen :7443 --target 127.0.0.1:22 \
    --authorized-keys /etc/tingly/authorized_keys \
    --state-dir /var/lib/tingly-ssh
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6
MemoryMax=512M

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now tingly-ssh
journalctl -u tingly-ssh | grep -E 'server listening|authorized keys loaded'
# ... msg="server listening" addr=[::]:7443 targets=127.0.0.1:22 pin=sha256:XUYr8w...
# ... msg="authorized keys loaded" count=3 labels=alice@laptop,bob@mbp,ca:corp-user-ca
```

**记下 `pin=sha256:…`**，客户端第一次连接时要拿它核对（§3.2）。

## 3. 客户端（每台设备）

### 3.1 确认钥匙在 agent 里

```bash
ssh-add -l
# 256 SHA256:+kPb2A9/0mCA... alice@laptop (ED25519)
```

输出 `The agent has no identities` 时，先运行 `ssh-add`（macOS 可用 `ssh-add --apple-use-keychain`）。

> **坑**：`AddKeysToAgent yes` 解决不了这个问题。它要等 ssh 登录成功之后才把钥匙放进 agent，
> 但 ProxyCommand 在登录之前就要签名。第一次必须手动 `ssh-add`，或者见 §4 的 `--identity`。

### 3.2 并排试用，不动原来的配置

先加一个**新别名**。原来的 `myserver` 原封不动，随时可以退回：

```sshconfig
# ~/.ssh/config
Host myserver                     # 原来的，保持不变
    HostName 203.0.113.10
    User alice

Host myserver-roam                # 新增：同一台机器，走隧道
    HostName 203.0.113.10
    User alice
    ProxyCommand tingly-ssh proxy --server %h:7443
```

第一次连接：

```console
$ ssh myserver-roam
time=... level=WARN msg="trusting this server key from now on" server=203.0.113.10:7443 pin="sha256:XUYr8w..." file=/home/alice/.config/tingly-ssh/known_servers
alice@myserver:~$
```

这行告警和 ssh 首次连接时的 "Are you sure you want to continue connecting" 是一回事：
**拿它的 `pin` 和 §2.3 记下的核对一次**，一致就好，之后不会再提示。

核对这一步不是非做不可，但建议做。即使首次连接恰好碰上中间人，对方也拿不到可复用的东西，
因为签名绑定在那条连接上；里层 ssh 的 `known_hosts` 也会照常报警。
只有核对过，才能确定记下的就是你自己的服务器。
如果想完全跳过"首次信任"，把 pin 直接写进配置：`--pin sha256:XUYr8w...`。

`proxy` 默认日志级别是 `warn`：连接正常时终端上什么都不打印，只有首次信任的告警、重连失败和各类错误才会显示。

### 3.3 验证漫游真的生效

```bash
ssh myserver-roam
# 在远端跑点持续输出的东西
while true; do date; sleep 1; done
```

然后在本机：

- 关掉 Wi-Fi 换到手机热点，或者合上盖子过一会儿再打开；
- 或者直接模拟一次"网络切换"：`pkill -USR1 -f 'tingly-ssh proxy'`。

`date` 应当在短暂停顿后继续输出，不丢行，也不需要重新登录。
断网时间默认不能超过 60 秒（`--session-linger`），超过了会话会被放弃，ssh 正常退出。

### 3.4 转正

试用满意后，把 `ProxyCommand` 那一行挪进原来的 `Host myserver`，删掉 `myserver-roam`。
**要回退，删掉这一行就行**，服务器那边什么都不用改。

多台服务器可以用通配符一次配好：

```sshconfig
Host *.prod.example.com
    ProxyCommand tingly-ssh proxy --server %h:7443
```

跳板机场景只给第一跳加，后面照常用 `ProxyJump`（细节见 [08-jump-host-topologies.md](08-jump-host-topologies.md)）：

```sshconfig
Host jump
    HostName jump.example.com
    ProxyCommand tingly-ssh proxy --server %h:7443

Host internal-*
    ProxyJump jump
```

## 4. 常见情况与对应写法

| 情况 | 写法 |
| --- | --- |
| agent 里钥匙很多，只想让隧道用其中一把 | `--identity ~/.ssh/id_ed25519.pub`。默认会拿 agent 里（除 FIDO 外）最多 8 把钥匙各签一次，服务端接受第一把命中的 |
| 没有 agent，私钥也没设密码 | `--identity ~/.ssh/id_ed25519`，直接读私钥。私钥有密码时必须先放进 agent |
| **FIDO 硬件钥匙**（`sk-ssh-ed25519`） | 默认不会用，因为每签一次都要碰一下。要用就显式指定：`--identity ~/.ssh/id_ed25519_sk.pub`。**每个会话只碰一次**，之后换网重连用服务端发的恢复凭证，不再签名 |
| **1Password / Secretive / KeePassXC** 等 agent，且 ssh_config 里用的是 `IdentityAgent` | **坑**：`IdentityAgent` 不会传给 ProxyCommand，ProxyCommand 只看 `SSH_AUTH_SOCK` 环境变量。要在 ProxyCommand 里自己带上：`ProxyCommand env SSH_AUTH_SOCK=$HOME/.1password/agent.sock tingly-ssh proxy --server %h:7443`（路径换成你的 agent）。这类 agent 每次签名都要确认的话，也是每个会话只确认一次 |
| 服务器上 SSH 端口不是 22 | 服务端改 `--target 127.0.0.1:2222`；客户端的 `Port` 不用改 |
| 隧道服务端和 ssh 的 `HostName` 不是同一台机器 | 把 `%h` 换成隧道服务端的地址：`--server tunnel.example.com:7443` |
| 服务端换了机器或重装，隧道服务端公钥变了 | 客户端会立刻拒绝，并告诉你删 `known_servers` 的第几行；核对新 pin 后删掉那一行即可 |
| 公司网络封了 UDP | 隧道连不上。保留一个不带 ProxyCommand 的别名作为后备 |

## 5. 撤销与离职

- 删掉 `/etc/tingly/authorized_keys` 里对应的行，再 `sudo systemctl reload tingly-ssh`（即 SIGHUP）。
  之后这把钥匙既不能新建连接，**它已有的会话也无法再重连**（重连时服务端会重新检查白名单）。
- 这是**隧道层**的撤销。sshd 那边的 `authorized_keys` 照常要删，两者互不替代。
- 已经连着的那条链路不会被踢掉（`04-security-model.md` §7）；要立刻断开，重启服务端。

## 6. 排错：错误信息 → 原因 → 怎么办

| 看到的信息 | 原因 | 怎么办 |
| --- | --- | --- |
| `SSH_AUTH_SOCK is not set` | 没有 agent，或者 ProxyCommand 拿不到它 | 启动 agent 并 `ssh-add`；用 `IdentityAgent` 的见 §4 |
| `ssh-agent holds no usable key (FIDO keys need --identity); run ssh-add` | agent 是空的，或者里面只有 FIDO 钥匙 | `ssh-add`；FIDO 钥匙加 `--identity …_sk.pub` |
| `… is not loaded in ssh-agent; run ssh-add …` | `--identity` 指向的钥匙不在 agent 里 | 按提示 `ssh-add` |
| `handshake refused (unauthorized): no offered SSH key is authorized` | 你的公钥不在隧道白名单里 | 管理员把你的公钥行加进 `/etc/tingly/authorized_keys` 并 reload |
| `handshake refused (unauthorized): SSH key expired` | 该行的 `expiry-time` 已过 | 管理员续期 |
| `handshake refused (unauthorized): SSH key is no longer authorized` | 会话中途，钥匙被撤销了 | 找管理员 |
| `this server does not accept SSH keys; use --token-file` | 服务端没配 `--authorized-keys` | 服务端加上这个参数 |
| `server key changed for …; … delete line N of …/known_servers` | 隧道服务端的公钥和记录的不一致 | 如果是你们自己换的，核对新 pin 后删掉第 N 行；**否则当作攻击处理** |
| 一直 `reconnect failed … await HELLO_ACK` | 服务端版本太旧，不支持钥匙认证 | 升级服务端 |
| 一直 `reconnect failed … timeout: handshake did not complete in time` | UDP 端口不通 | 检查防火墙和安全组的 **UDP** 规则 |
| ssh 报 `Host key verification failed` | 这是里层 SSH 的 `known_hosts`，和隧道无关 | 与直连时的处理方式相同 |

隧道客户端的日志打在 ssh 的 stderr 上，`proxy` 默认只打 `warn` 及以上。
想看每次建链，加 `--log-level info`：每次都会打印一行 `link established`，行尾的 `auth=ssh-key`
表示签名建链，`auth=ticket` 表示用恢复凭证重连。要看更细的过程，用 `--log-level debug`。
