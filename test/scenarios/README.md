# test/scenarios — 日常 SSH 场景套件

把真实用户每天会做的 SSH 操作全部变成可复现的自动用例，跑在隧道之上。
**不改动系统和用户的 SSH 配置**：sshd 用脚本生成的独立配置启动，ssh 通过 `-F` 接入。

```bash
./run.sh            # 全部场景，约 3 分钟
./run.sh --quick    # 跳过 S3/S13 两个慢用例
./run.sh S2 S6      # 只跑指定场景
```

| ID | 场景 | 关键断言 |
| --- | --- | --- |
| S1 | 交互式终端 | 分配了真 tty，命令输出正确，干净退出 |
| S2 | **交互式会话中链路被销毁** | 同一个 shell 里断链前后都能输入并拿到结果 |
| S3 | 大量标准输出 | 32 MiB 输出 sha256 一致 |
| S4 | 8-bit 透明 | 全部 256 种字节值往返不变 |
| S5 | 流分离与退出码 | stdout/stderr 不混，退出码 42 透传 |
| S6 | Ctrl-C | 打断前台任务，shell 存活，被打断的命令没有执行 |
| S7 | 窗口尺寸变化 | SIGWINCH 传到远端，`stty size` 跟着变 |
| S8 | `ssh -L` 本地转发 | 转发端口能通 |
| S9 | `ssh -R` 远程转发 | 远端回连本地服务能通 |
| S10 | `ssh -D` SOCKS5 | SOCKS 代理能通 |
| S11 | sftp | put + get 4 MiB，sha256 一致 |
| S12 | ControlMaster 复用 | 3 个会话只占 **1 条**隧道流 |
| S13 | 空闲后仍可用 | 空闲后交互式 shell 继续响应 |
| S14 | rsync over ssh | 8 MiB，sha256 一致（未装 rsync 则 SKIP） |

## 交互式用例怎么写

`ptydrive.py` 在真 pty 上驱动 ssh，步骤脚本放在 `steps/`，一行一步：

```
expect "[$#] $"            # 等提示符
send "echo S1-MARK-$((20+22))"
expect "S1-MARK-42"
ctrl C                     # 发 Ctrl-C
resize 40 100              # 改窗口尺寸，触发 SIGWINCH
touch @ART@/S2.ready       # 把控制权交给外面的脚本
waitfile @ART@/S2.go 120   # 等外面的脚本做完动作
expect_exit 0
```

**标记为什么用算术**：终端会回显你输入的命令，所以 `expect "MARK"` 会匹配到自己的回显、
什么都证明不了。写成 `echo MARK-$((20+22))` 后，回显里是表达式、输出里才是 `MARK-42`，
断言才真正检查了远端执行结果。这个坑在 S6 上真实发生过一次。

依赖：`ssh`、`/usr/sbin/sshd`、`python3`（驱动 pty）、可选 `rsync`。
结果与全部日志、transcript 归档在 `artifacts/<时间戳>/`。
