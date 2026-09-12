# test/roaming — 真机漫游验收套件

把原先只能手工做的漫游用例变成可执行脚本。物理动作（关 Wi-Fi、飞行模式）是唯一
无法跨平台脚本化的部分，它被收进 `netctl.sh` 的四个动作里；**测量、判定、取证全部自动**。

```bash
cp roaming.env.example roaming.env   # 填你的部署
./run.sh                             # 全部用例
./run.sh --quick                     # 跳过 R5/R6（慢）
./run.sh R1 R3                       # 只跑指定用例
```

| ID | 场景 | 通过标准 |
| --- | --- | --- |
| R1 | 离开当前网络（Wi-Fi → 蜂窝） | 字节流连续、会话继续、卡顿 ≤ `MAX_STALL` |
| R2 | 回到原网络 | 同上 |
| R3 | 断网 `OUTAGE_SECONDS`（< linger） | 会话恢复，字节流无断点 |
| R4 | 断网超过 linger | ssh **明确失败**不挂死，客户端日志有 `giving up on session` |
| R5 | 空闲 `IDLE_SECONDS`（NAT 超时） | 之后仍可用；若路径被重建会提示调大 keepalive |
| R6 | 传输中途换网 | `BULK_MB` 文件 sha256 一致 |
| R7 | 隧道服务端重启 | 明确失败（`SESSION_UNKNOWN` 或收到 CLOSE），不挂死 |

## 怎么测量

远端跑一个计数器心跳，每 0.2s 一行；本地给每行打到达时间戳。于是：

- **计数器连续** → 会话层字节流不丢、不重、不乱（这是恢复正确性的证明）；
- **最大到达间隔** → 用户真正看到的终端卡顿时长（写进结果表）；
- **客户端日志里的 `link lost` / `link established`** → 这次是 QUIC 迁移（没丢链路）
  还是会话层重连（丢了又接上）。两者都算通过，但结果会如实标明是哪一种。

判定只在三种情况下失败：字节流有断点、事件后会话没再继续、卡顿超过预算。

## 网络控制器

| `NET_CTL` | 说明 |
| --- | --- |
| `nmcli` | Linux + NetworkManager，全自动 |
| `macos` | macOS，全自动（需要第二条路径，例如蜂窝或热点） |
| `manual` | 任何系统：提示操作者动手，其余仍然自动测量 |
| `sim` | 没有无线电：销毁链路 / 冻结服务端，用于自测 harness 本身 |

`auto`（默认）会挑 nmcli 或 macos，都没有就退到 manual。

## 自测：证明 harness 本身是对的

```bash
./selftest.sh          # 本地起 sshd + 隧道服务端，用 NET_CTL=sim 跑全部用例
```

`sim` 下"换网"= 销毁 QUIC 链路（给客户端发 `SIGUSR1`），"断网"= 冻结服务端进程
（`SIGSTOP`，包发得出去但没有任何回应）。**测量与判定代码与真机完全相同**，
只有那四个网络动作不一样。所以在没有无线电的环境里也能证明套件可用。

客户端参数默认 `--idle-timeout 8s --keepalive 2s`：笔记本希望几秒内发现链路已死，
而不是等 QUIC 默认的 20 秒。代价是多一点保活流量。
