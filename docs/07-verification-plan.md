# 07 · 验证方案（留存版）

本文件是**验收的唯一清单**：每个用例有稳定 ID、前置条件、步骤、判定标准，
以及"由谁执行"（自动 / 手工）。自动用例全部落在 `test/e2e/`，每次运行的日志与结果
留存在 `test/e2e/artifacts/<时间戳>/`。

## 0. 总原则

1. **不改动 SSH 的任何一端**。sshd 用它自己的独立配置文件启动（不碰系统 `/etc/ssh/sshd_config`），
   ssh 用 `-F` 指定的额外配置接入（不碰 `~/.ssh/config`）。隧道只以两种方式"补上去"：
   - `ProxyCommand`（推荐，无本地监听端口）；
   - 本地 TCP 监听端口（`ssh -p 2222 127.0.0.1`）。
2. **每个用例必须能证伪**。例如"断网后恢复"不仅要看 ssh 会话还活着，还要断言
   日志里确实出现过 `link lost` 与新的 `link established`，否则说明故障根本没注入进去。
3. **结果留存**。自动跑出的 `results.md` 连同全部日志归档；手工用例的结论回填到本文 §4 的表格。

## 1. 分层

| 层 | 位置 | 运行方式 | 覆盖 |
| --- | --- | --- | --- |
| L0 单元 | `internal/proto` | `go test ./...` | 帧编解码 round-trip、截断/超长/非法输入 |
| L1 会话层 | `internal/mux` | `go test -race ./...` | 不变量 I1–I5、半关闭、RESET、窗口、epoch |
| L2 故障注入 | `internal/mux` | 同上 | 内存管道链路在传输中途反复切断后逐字节比对 |
| L3 隧道端到端 | `internal/bridge` | 同上 | 真 QUIC + 真 TCP，`DropLink()` 注入链路销毁，token/pin/权限 |
| **L4 真实 SSH 端到端** | `test/e2e/` | `./test/e2e/run.sh` | 真 sshd + 真 ssh/scp，见 §2 |
| **L5 日常 SSH 场景** | `test/scenarios/` | `./test/scenarios/run.sh` | 交互式 pty、大输出、转发、sftp、复用，见 §3 |
| **L6 漫游** | `test/roaming/` | `./test/roaming/run.sh` | 换网、断网、NAT 超时、服务端重启，见 §4 |

## 2. L4 用例表（自动，`test/e2e/run.sh`）

环境由脚本自建：两个专用 sshd（2022 扮演跳板机、2224 扮演其后的目标机，各自独立配置与 host key）、
一个 TCP 回显服务（2023）、隧道服务端（udp 7450）、按需启动的隧道客户端（2300+）；
E15–E18 另起一个只认 SSH 密钥的隧道服务端（udp 7452）和一个私有 ssh-agent（socket 在 `/tmp` 下）。

| ID | 名称 | 步骤要点 | 通过标准 |
| --- | --- | --- | --- |
| E1 | 隧道承载普通 TCP + 半关闭 | 经隧道连回显服务，`shutdown(SHUT_WR)` | 收到全部回显且对端把 EOF 正确传下去 |
| E2 | ssh over ProxyCommand | `ssh -o ProxyCommand='tingly-ssh proxy …' host cmd` | 命令输出正确，sshd 日志显示正常认证 |
| E3 | ssh over 本地监听端口 | `ssh -p <client-port> 127.0.0.1 cmd` | 同上 |
| E4 | stdin 转发与 EOF 传播 | `ssh host cat < 200KB 文件` | 回传字节与原文件 `cmp` 一致 |
| E5 | scp 大文件完整性 | `scp -P <port> 8MiB 文件` | sha256 一致 |
| E6 | 并发会话复用 | 同时发起 3 个 ssh | 3 个逻辑流、**1 个** session，全部成功 |
| E7 | 活动会话中链路被销毁 | 流式输出期间两次 `kill -USR1` 客户端 | 输出 20/20 行不丢不乱；日志有 2 次强制断链、≥3 次建链 |
| E8 | 30 秒"断网"后恢复 | `SIGSTOP` 冻结服务端 30s（等价黑洞） | 输出 55/55 行；日志出现 `link lost` 且重新建链 |
| E9 | 超过 linger 的断网 | linger 5s，冻结 25s | ssh **明确失败**且不挂死；日志出现 `giving up on session` |
| E10 | token 错误 | 用错误 token 启动客户端 | 立即失败（0 次重试），日志给出 `unauthorized` 原因 |
| E11 | 证书 pin 不匹配 | 用随机 pin 启动客户端 | 握手被拒，日志给出 `pin mismatch` |
| E12 | 跳板机两跳 | `ssh -J` 经未改动的跳板机到目标机，第一跳走隧道 | 目标机上的命令输出正确 |
| E13 | 两跳会话中断链 | 两跳会话进行中两次 `kill -USR1` | 目标机输出 20/20 行，2 次强制断链 |
| E14 | **每设备凭据与撤销** | 两台设备各自的 token；删掉一行后 `SIGHUP` | 两台都能连且日志归因到 label；被撤销的那台立刻被拒 |
| E15 | **只用 ssh-agent 登录** | `ProxyCommand tingly-ssh proxy --server …`，无 token、无 pin；隧道白名单就是 sshd 的 authorized_keys | ssh 成功；服务端日志归因到公钥注释；首次写入 known_servers 并告警；proxy 默认 warn，终端上无 INFO 行，第二次 stderr 为空 |
| E16 | key 会话中断链 | 流式输出期间两次 `kill -USR1` | 20/20 行；客户端日志 1 次 `auth=ssh-key`、≥2 次 `auth=ticket`（重连不重签） |
| E17 | 未授权的 key | `--identity` 指向不在白名单里的私钥（不用 agent） | 立即失败（0 次重试），原因 `no offered SSH key is authorized` |
| E18 | 服务端换钥（TOFU） | 同地址换一把新服务端密钥 | 客户端立即拒绝（0 次重试），报出 known_servers 的行号；服务端 0 次 `link accepted` |

运行方式：

```bash
./test/e2e/run.sh              # 全部用例（约 3 分钟）
./test/e2e/run.sh --quick      # 跳过 E8/E9 两个慢用例
./test/e2e/run.sh E7 E8        # 只跑指定用例
```

前置条件：`go`、`ssh`、`/usr/sbin/sshd`、`python3`。脚本自己编译二进制到
`test/e2e/.bin/`，也可用 `TINGLY_BIN=/path/to/tingly-ssh` 指定既有二进制。

### 2.1 故障是怎么注入的

| 手法 | 模拟的真实故障 | 为什么可信 |
| --- | --- | --- |
| `kill -USR1 <client>` | 换网导致旧路径立刻失效 | 客户端真的销毁当前 QUIC 连接并重新拨号，走完整握手与重放 |
| `kill -STOP <server>` | 断网/黑洞：包发得出去但没有任何回应 | 进程被冻结，UDP 不再被处理，客户端侧与真断网不可区分 |
| `Session.DropLink()`（L3） | 同上，进程内版本 | 供 Go 测试使用，无需子进程 |

`SIGUSR1` 不是测试专用后门：M3 会用同一个入口响应操作系统的"默认路由变了"事件，
主动换路而不是等旧路径超时。

## 3. L5 日常 SSH 场景（自动，`test/scenarios/run.sh`）

SSH 最常用的就是"开一个终端敲命令"和"把数据倒过去"，这些必须有场景兜住，
否则单元测试全绿也说明不了产品可用。交互式用例由 `ptydrive.py` 在**真 pty** 上驱动。

| ID | 场景 | 通过标准 |
| --- | --- | --- |
| S1 | 交互式终端 | 分配了真 tty，命令输出正确，干净退出 |
| S2 | **交互式会话中链路被销毁** | 同一个 shell 里断链前后都能输入并拿到结果 |
| S3 | 大量标准输出 | 32 MiB 输出 sha256 一致 |
| S4 | 8-bit 透明 | 全部 256 种字节值往返不变 |
| S5 | 流分离与退出码 | stdout/stderr 不混，退出码 42 透传 |
| S6 | Ctrl-C | 打断前台任务，shell 存活，被打断的命令确实没执行 |
| S7 | 窗口尺寸变化 | SIGWINCH 传到远端，`stty size` 跟着变 |
| S8 | `ssh -L` 本地转发 | 转发端口能通 |
| S9 | `ssh -R` 远程转发 | 远端回连本地服务能通 |
| S10 | `ssh -D` SOCKS5 | SOCKS 代理能通 |
| S11 | sftp put/get | 4 MiB，sha256 一致 |
| S12 | ControlMaster 复用 | 3 个会话只占 **1 条**隧道流 |
| S13 | 空闲后仍可用 | 空闲后交互式 shell 继续响应 |
| S14 | rsync over ssh | 8 MiB sha256 一致（未装 rsync 则 SKIP） |

### 3.1 tmux 三个用例分别证明什么

tmux 是最常用的用法，它和本项目的职责必须分清楚，否则容易互相冒功：

| 用例 | 谁在起作用 |
| --- | --- |
| S15 | 纯 tmux：分离与重连，隧道只是通路 |
| S16 | **隧道**：链路被销毁期间会话不断，用户只看到短暂停顿，tmux 无需重连 |
| S17 | **tmux**：连接彻底死透（ssh 被 SIGKILL），远端工作照常推进，人回来后重连 |

S16 是隧道的价值：不用重连、不用重新 attach。S17 是 tmux 的价值：即使隧道也救不回来
（超过 linger、换了设备），工作不丢。两者叠加才是完整的日常工作流。

S17 踩过一个坑：最初用 `pkill -f "<会话名>"` 杀客户端，结果把 tmux 自己也杀了
（同机测试时会话名出现在多个进程的命令行里），于是"工作继续"变成了假失败。
现在只按驱动进程和本套件的 ssh 命令行精确匹配。

### 3.2 只有 CI 能抓到的一类失败（踩过）

S12（ControlMaster 复用）在本地一直通过，在 CI 上必然失败：

```
unix_listener: path ".../artifacts/<run>/S12.sock.2WzkaW5RbrAHkUjg" too long for Unix domain socket
```

Unix domain socket 路径上限约 104 字节，ssh 在启动 master 期间还会追加 17 字符随机后缀。
CI runner 的检出路径（`/home/runner/work/<repo>/<repo>/…`）比开发机长，于是越界。
控制 socket 因此必须放在 `mktemp -d` 的短目录里，不能放进 `$ART`。

这类"只在别的路径长度/环境下才失败"的问题，正是把套件接进 CI 的价值：
本地全绿不等于可移植。修复流程也照规矩走——**先用长 `ART_DIR` 在本地复现**，
再改，再用同一个长路径验证通过。

顺带暴露了 harness 会挂死而不是失败的一个缺陷：`ssh-keygen` 没有 force 参数，
复用 artifacts 目录时它会提示覆盖并无限等待 stdin。现在生成前先删旧文件并把 stdin
接到 `/dev/null`。

### 3.3 交互式断言的一个陷阱（踩过）

终端会回显输入的命令，所以 `expect "MARK"` 会匹配到**自己的回显**，什么都没验证。
步骤脚本因此统一用算术标记：输入 `echo MARK-$((20+22))`，回显里是表达式、
输出里才是 `MARK-42`。S6 最初就是因为这个假通过（甚至假失败），修正后才真正检查了
"被 Ctrl-C 打断的命令没有执行"。

## 4. L6 漫游（自动化，`test/roaming/run.sh`）

原先只能手工的漫游用例现在是脚本。物理动作（关 Wi-Fi、飞行模式）收进 `netctl.sh`
的四个动作里，其余全部自动。

| ID | 原手工用例 | 场景 | 通过标准 |
| --- | --- | --- | --- |
| R1 | M1 | 离开当前网络（Wi-Fi → 蜂窝） | 字节流连续、会话继续、卡顿 ≤ `MAX_STALL` |
| R2 | M2 | 回到原网络 | 同上 |
| R3 | M3 | 断网 `OUTAGE_SECONDS`（< linger） | 会话恢复，字节流无断点 |
| R4 | M4 | 断网超过 linger | ssh 明确失败不挂死，日志有 `giving up on session` |
| R5 | M5 | 空闲 `IDLE_SECONDS`（NAT 超时） | 之后仍可用；路径被重建会提示调大 keepalive |
| R6 | M6 | 传输中途换网 | `BULK_MB` 文件 sha256 一致 |
| R7 | M7 | 隧道服务端重启 | 明确失败（`SESSION_UNKNOWN` 或收到 CLOSE），不挂死 |

### 4.1 怎么把"看起来还活着"变成可判定的测量

远端跑计数器心跳（每 0.2s 一行），本地给每行打到达时间戳，于是：

| 测量 | 证明什么 |
| --- | --- |
| 计数器连续（无缺号、无重号） | 会话层字节流不丢不重不乱，即恢复语义正确 |
| 最大到达间隔 | 用户真正看到的终端卡顿时长，写进结果表 |
| 日志里 `link lost` / `link established` | 这次是 QUIC 迁移（没丢链路）还是会话层重连 |

判定只在三种情况失败：字节流有断点、事件后会话没再继续、卡顿超过预算。
"是迁移还是重连"不作为判定条件，但如实写进结果——接入 Path API（M3）后这一栏会从
"重连"变成"迁移"，届时不需要改测试。

### 4.2 网络控制器与 harness 自测

| `NET_CTL` | 说明 |
| --- | --- |
| `nmcli` | Linux + NetworkManager，全自动 |
| `macos` | macOS，全自动（需要第二条路径，例如蜂窝/热点） |
| `manual` | 任何系统：提示操作者动手，其余仍然自动测量 |
| `sim` | 没有无线电：销毁链路 / 冻结服务端，用于自测 harness 本身 |

```bash
./test/roaming/selftest.sh     # 本地 sshd + 隧道服务端，NET_CTL=sim 跑全部 R 用例
```

`sim` 下"换网"= 给客户端发 `SIGUSR1` 销毁 QUIC 链路，"断网"= `SIGSTOP` 冻结服务端
（包发得出去但没有任何回应）。**测量与判定是同一份代码**，只有四个网络动作不同，
所以在 CI 这种没有无线电的环境里也能证明套件本身可用、不会假通过。

真机上仍然需要人做的只剩一件事：在没有 `nmcli`/`networksetup` 的系统上按提示切网络。

## 5. 判定与归档

每次运行输出：

```
test/e2e/artifacts/<时间戳>/
  results.md          # 用例结果表 + 环境信息（commit、go、ssh 版本）
  tunnel-server.log   # 隧道服务端日志
  client-<case>.log   # 每个用例自己的客户端日志
  sshd.log            # sshd 的 VERBOSE 日志
  E*.out / E*.err     # 每个用例的原始输出
```

退出码：全部通过为 0，任一失败为 1（可直接接 CI）。

### 5.1 最近一次归档结果（L4 套件）

- run: 20260912-141308
- host: Linux 6.18.44-fc-v24 x86_64
- go: go1.26.0
- commit: f0106d3
- ssh: OpenSSH_9.6p1 Ubuntu-3ubuntu13.19

| case | result | note |
| --- | --- | --- |
| E1 | PASS | TCP echo + half close through the tunnel |
| E2 | PASS | ssh -o ProxyCommand reached sshd as root |
| E3 | PASS | ssh -p 2301 through the local listener |
| E4 | PASS | 200000 bytes over stdin, EOF propagated |
| E5 | PASS | 8 MiB scp, sha256 a305f1fc02e9 |
| E6 | PASS | 3 concurrent ssh sessions as 3 streams of 1 session |
| E7 | PASS | 20/20 lines across 2 forced link failures (3 links) |
| E8 | PASS | 55/55 lines across a 30s outage (1 link losses, 2 links) |
| E9 | PASS | gave up after the 5s linger; ssh failed cleanly in 28s with 3 lines |
| E10 | PASS | refused as unauthorized in 0s, no retries |
| E11 | PASS | pinned handshake refused, client exited in 8s |
| E12 | PASS | ssh -J through an unmodified jump host, over the tunnel |
| E13 | PASS | two hop session survived 2 first-hop link failures |
| E14 | PASS | two devices admitted and attributed, revoked one refused in 0s |

> E8 的判定要求日志里确实出现过 `link lost`，所以那一行的数字证明 30 秒黑洞真的杀死了
> QUIC 连接，会话是**重连后重放恢复**的，而不是碰巧没断。E12/E13 用两个独立 sshd
> 实例验证多跳，跳板机侧零改动。E14 覆盖每设备凭据与撤销。

### 5.2 最近一次归档结果（L5 场景套件）

- run: 20260912-141506
- host: Linux 6.18.44-fc-v24 x86_64
- commit: f0106d3
- ssh: OpenSSH_9.6p1 Ubuntu-3ubuntu13.19

| case | result | note |
| --- | --- | --- |
| S1 | PASS | interactive shell with a real tty, clean exit |
| S2 | PASS | typed before and after 2 link failures in one shell |
| S3 | PASS | 32 MiB of stdout, sha256 564e1971cb2c |
| S4 | PASS | all 256 byte values round tripped unchanged |
| S5 | PASS | streams separate, exit status 42 propagated |
| S6 | PASS | Ctrl-C killed the remote sleep, shell survived |
| S7 | PASS | SIGWINCH propagated, remote stty size followed |
| S8 | PASS | ssh -L carried a TCP service over the tunnel |
| S9 | PASS | ssh -R reached back through the tunnel |
| S10 | PASS | ssh -D SOCKS5 proxied through the tunnel |
| S11 | PASS | sftp put and get, 4 MiB, sha256 f5f485681832 |
| S12 | PASS | 3 multiplexed sessions over 1 tunnel stream |
| S13 | PASS | shell still responsive after 30s idle |
| S14 | PASS | rsync over ssh, 8 MiB, sha256 3d9eef74f043 |
| S15 | PASS | detached with Ctrl-B d, session and pane content survived |
| S16 | PASS | tmux output continued across 2 link failures |
| S17 | PASS | work kept running after a hard kill (22 -> 54 lines), pane reattachable |

### 5.3 最近一次归档结果（L6 漫游套件，`selftest.sh` / NET_CTL=sim）

- run: 20260912-141614
- host: Linux 6.18.44-fc-v24 x86_64
- network controller: sim
- server: 127.0.0.1:7470  target: 127.0.0.1:2042
- client flags: --session-linger 15s --idle-timeout 8s --keepalive 2s
- thresholds: max stall 15s, outage 12s, idle 20s
- commit: f0106d3

| case | result | note |
| --- | --- | --- |
| R1 | PASS | survived, froze 0.2s, 62 lines after the event, 1 link loss(es), 2 links |
| R2 | PASS | survived, froze 0.2s, 62 lines after the event, 1 link loss(es), 2 links |
| R3 | PASS | survived, froze 12.2s, 122 lines after the event, 1 link loss(es), 2 links |
| R4 | PASS | gave up after the 15s linger, ssh failed cleanly in 45s |
| R5 | PASS | path held through 20s idle, no reconnect |
| R6 | PASS | 8 MiB transfer crossed a network change, sha256 f08437afad0e |
| R7 | PASS | server said it was shutting down, client stopped cleanly |

> note 里写明了每次是"迁移"还是"重连"，以及实测卡顿秒数。真机数字请用
> `./test/roaming/run.sh` 重跑后替换本节。

### 5.4 CI

`.github/workflows/ci.yml` 在每次 push 与 PR 上跑四个 job，并上传各自的 artifacts：

| job | 内容 | 典型耗时 |
| --- | --- | --- |
| `unit` | `gofmt -l`、`go vet`、`go test -race` | ~25s |
| `e2e` | `./test/e2e/run.sh`（真 sshd） | ~2.5min |
| `scenarios` | `./test/scenarios/run.sh`（含 pty、tmux、rsync） | ~1.7min |
| `roaming-harness` | `./test/roaming/selftest.sh`（NET_CTL=sim） | ~3min |

## 6. 回归门槛

合并任何改动前必须通过：

```bash
go test -race ./...            # L0–L3
./test/e2e/run.sh              # L4
./test/scenarios/run.sh        # L5
./test/roaming/selftest.sh     # L6 的 harness 自测（真机漫游另行安排）
```

线格式（`docs/02-wire-protocol.md`）有任何改动时，额外要求：新旧版本互连的
拒绝行为被 E10/E11 同款断言覆盖，即**拒绝必须带原因且客户端不再重试**。
