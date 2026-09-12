# test/e2e — 真实 SSH 端到端验证

跑真的 `sshd` 和真的 `ssh`/`scp`，验证隧道能在不改动 SSH 双端的前提下把它们连起来，
并在链路被销毁、网络黑洞、认证失败等情况下表现正确。

```bash
./run.sh            # 全部用例，约 3 分钟
./run.sh --quick    # 跳过 E8/E9（慢用例）
./run.sh E7 E8      # 只跑指定用例
```

- 用例清单与判定标准：`docs/07-verification-plan.md` §2
- 依赖：`go`、`ssh`、`/usr/sbin/sshd`、`python3`
- 复用已有二进制：`TINGLY_BIN=/path/to/tingly-shell ./run.sh`
- 结果与全部日志：`artifacts/<时间戳>/`（`results.md` 是结果表）

**不改动 SSH 双端**：sshd 用脚本生成的独立 `sshd_config`（自带 host key 与
authorized_keys）启动，系统配置不动；ssh 用 `-F artifacts/<ts>/ssh_config` 接入，
`~/.ssh/config` 不动。隧道只以 `ProxyCommand` 或本地监听端口两种方式补上去。

`artifacts/` 不进版本库（见 `.gitignore`）；需要留存某次结果时，把该次的
`results.md` 贴进 `docs/07-verification-plan.md` 或提交到别处。
