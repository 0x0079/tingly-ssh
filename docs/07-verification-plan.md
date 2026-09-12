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
| L5 真机漫游 | 手工 | 见 §4 | Wi-Fi↔蜂窝、地铁断网、NAT 超时 |

## 2. L4 用例表（自动，`test/e2e/run.sh`）

环境由脚本自建：专用 sshd（端口 2022，自带 host key 与 authorized_keys）、
一个 TCP 回显服务（2023）、隧道服务端（udp 7450）、按需启动的隧道客户端（2300+）。

| ID | 名称 | 步骤要点 | 通过标准 |
| --- | --- | --- | --- |
| E1 | 隧道承载普通 TCP + 半关闭 | 经隧道连回显服务，`shutdown(SHUT_WR)` | 收到全部回显且对端把 EOF 正确传下去 |
| E2 | ssh over ProxyCommand | `ssh -o ProxyCommand='tingly-shell proxy …' host cmd` | 命令输出正确，sshd 日志显示正常认证 |
| E3 | ssh over 本地监听端口 | `ssh -p <client-port> 127.0.0.1 cmd` | 同上 |
| E4 | stdin 转发与 EOF 传播 | `ssh host cat < 200KB 文件` | 回传字节与原文件 `cmp` 一致 |
| E5 | scp 大文件完整性 | `scp -P <port> 8MiB 文件` | sha256 一致 |
| E6 | 并发会话复用 | 同时发起 3 个 ssh | 3 个逻辑流、**1 个** session，全部成功 |
| E7 | 活动会话中链路被销毁 | 流式输出期间两次 `kill -USR1` 客户端 | 输出 20/20 行不丢不乱；日志有 2 次强制断链、≥3 次建链 |
| E8 | 30 秒"断网"后恢复 | `SIGSTOP` 冻结服务端 30s（等价黑洞） | 输出 55/55 行；日志出现 `link lost` 且重新建链 |
| E9 | 超过 linger 的断网 | linger 5s，冻结 25s | ssh **明确失败**且不挂死；日志出现 `giving up on session` |
| E10 | token 错误 | 用错误 token 启动客户端 | 立即失败（0 次重试），日志给出 `unauthorized` 原因 |
| E11 | 证书 pin 不匹配 | 用随机 pin 启动客户端 | 握手被拒，日志给出 `pin mismatch` |

运行方式：

```bash
./test/e2e/run.sh              # 全部用例（约 3 分钟）
./test/e2e/run.sh --quick      # 跳过 E8/E9 两个慢用例
./test/e2e/run.sh E7 E8        # 只跑指定用例
```

前置条件：`go`、`ssh`、`/usr/sbin/sshd`、`python3`。脚本自己编译二进制到
`test/e2e/.bin/`，也可用 `TINGLY_BIN=/path/to/tingly-shell` 指定既有二进制。

### 2.1 故障是怎么注入的

| 手法 | 模拟的真实故障 | 为什么可信 |
| --- | --- | --- |
| `kill -USR1 <client>` | 换网导致旧路径立刻失效 | 客户端真的销毁当前 QUIC 连接并重新拨号，走完整握手与重放 |
| `kill -STOP <server>` | 断网/黑洞：包发得出去但没有任何回应 | 进程被冻结，UDP 不再被处理，客户端侧与真断网不可区分 |
| `Session.DropLink()`（L3） | 同上，进程内版本 | 供 Go 测试使用，无需子进程 |

`SIGUSR1` 不是测试专用后门：M3 会用同一个入口响应操作系统的"默认路由变了"事件，
主动换路而不是等旧路径超时。

## 3. 判定与归档

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

### 3.1 最近一次归档结果

- run: 20260912-072740
- host: Linux 6.18.44-fc-v24 x86_64
- go: go1.26.0
- commit: 1cccf5e
- ssh: OpenSSH_9.6p1 Ubuntu-3ubuntu13.19

| case | result | note |
| --- | --- | --- |
| E1 | PASS | TCP echo + half close through the tunnel |
| E2 | PASS | ssh -o ProxyCommand reached sshd as root |
| E3 | PASS | ssh -p 2301 through the local listener |
| E4 | PASS | 200000 bytes over stdin, EOF propagated |
| E5 | PASS | 8 MiB scp, sha256 43ae6af16977 |
| E6 | PASS | 3 concurrent ssh sessions as 3 streams of 1 session |
| E7 | PASS | 20/20 lines across 2 forced link failures (3 links) |
| E8 | PASS | 55/55 lines across a 30s outage (1 link losses, 2 links) |
| E9 | PASS | gave up after the 5s linger; ssh failed cleanly in 28s with 3 lines |
| E10 | PASS | refused as unauthorized in 0s, no retries |
| E11 | PASS | pinned handshake refused, client exited in 7s |

> 说明：E8 的判定要求日志里确实出现过 `link lost`，因此 "1 link losses, 2 links"
> 证明 30 秒黑洞真的杀死了 QUIC 连接，会话是**重连后重放恢复**的，而不是碰巧没断。

## 4. L5 真机漫游验收（手工，结论回填此表）

自动化无法覆盖真实无线网卡切换，以下用例需要真机执行。

| ID | 场景 | 操作 | 期望 | 结论 |
| --- | --- | --- | --- | --- |
| M1 | Wi-Fi → 蜂窝 | ssh 里跑 `ping -i0.2`，关掉 Wi-Fi | 停顿 < 2s 后继续；日志一次重连 | 待执行 |
| M2 | 蜂窝 → Wi-Fi | 反向切换 | 同上 | 待执行 |
| M3 | 飞行模式 40s | 开飞行模式 40s 后恢复 | 会话继续，日志显示重放字节数 | 待执行 |
| M4 | 飞行模式 90s（linger 60s） | 超过 linger | ssh 明确断开，不挂死 | 待执行 |
| M5 | NAT 超时 | 空闲 10 分钟 | 开启 keepalive 时不断连 | 待执行 |
| M6 | 大文件 + 切网 | `scp` 1GB 中途切网 | 传输继续，sha256 一致 | 待执行 |
| M7 | 服务端重启 | 重启隧道服务端 | 客户端收到 `SESSION_UNKNOWN` 并明确退出（v0 预期行为） | 待执行 |

执行方法：客户端加 `--log-level debug` 并保留日志，按上表逐条记录，
把结论与日志路径回填到"结论"列，随提交一起留存。

## 5. 回归门槛

合并任何改动前必须通过：

```bash
go test -race ./...       # L0–L3
./test/e2e/run.sh         # L4
```

线格式（`docs/02-wire-protocol.md`）有任何改动时，额外要求：新旧版本互连的
拒绝行为被 E10/E11 同款断言覆盖，即**拒绝必须带原因且客户端不再重试**。
