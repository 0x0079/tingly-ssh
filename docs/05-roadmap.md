# 05 · 里程碑与进度

唯一的进度真相来源。勾选项变更必须与代码同一次提交。

## M0 · 骨架与规范（已完成）
- [x] 文档体系与 ADR（`docs/`）
- [x] Go module、目录结构、CI 可跑的 `go test ./...`

## M1 · 核心会话层（已完成）
- [x] `internal/proto`：帧编解码 + round-trip 测试 + 模糊输入健壮性
- [x] `internal/mux`：Session / Stream / 重放缓冲 / 偏移量流控
- [x] 半关闭（FIN）与 RESET 语义
- [x] Link 断开时 Stream 只阻塞不报错（I5）
- [x] 断链重连后字节流无丢失/无重复（I1/I2/I3）的单元测试
- [x] `epoch` 抢占与过期 Link 拒绝

## M2 · QUIC 承载与 bridge（已完成）
- [x] `internal/transport`：quic-go dial/listen、ALPN、自签证书生成、SPKI pin 校验
- [x] `internal/bridge`：client（TCP listener）/ proxy（stdio）/ server（dial target）
- [x] 客户端 Supervisor：指数退避 + jitter + linger 终止
- [x] 服务端 Session registry + reaper + token 认证
- [x] 端到端测试：QUIC 上跑真实 TCP 回环，含强制断链后恢复
- [x] CLI：`server` / `client` / `proxy` / `keygen`，客户端 `SIGUSR1` 主动换链路
- [x] 握手拒绝原因可达：refusal 帧先落地再关连接，客户端不再把"token 错"当成"网络断"
- [x] 真实 SSH 端到端验证套件 `test/e2e/`（E1–E13，见 `07-verification-plan.md`）
- [x] 跳板机多跳场景：与 `ProxyJump` 组合，跳板机零改动（`08-jump-host-topologies.md`，E12/E13）

## M2.5 · 验收体系（已完成）
- [x] 共享 harness 层 `test/lib/`（进程管理、等待、心跳测量、结果归档）
- [x] 日常 SSH 场景套件 `test/scenarios/`（S1–S17：交互式 pty、大输出、8-bit 透明、
      Ctrl-C、窗口尺寸、`-L`/`-R`/`-D`、sftp、rsync、ControlMaster 复用、空闲、
      tmux 分离重连 / 断链续跑 / 硬杀后重连）
- [x] 漫游套件 `test/roaming/`（R1–R7）+ 可插拔网络控制器（nmcli/macos/manual/sim）
- [x] 漫游 harness 自测 `selftest.sh`：无无线电环境下用 sim 控制器跑完整 R 用例
- [x] 心跳测量把"看起来还活着"变成可判定数字：计数器连续性 + 最大卡顿时长

## M3 · 生产加固（未开始）

安全项按 `04-security-model.md` §8 的编号排列，括号里是风险编号。

- [x] 客户端身份方案 A（ADR-0004 §8.1）：
      - [x] 每设备 token，服务端凭据文件存 `sha256(token)` + label + 可选 `not-after`
      - [x] `keygen --label/--expires` 打印 token 与可粘贴的服务端记录行
      - [x] `SIGHUP` 热加载凭据文件，撤销 = 删一行；解析失败保留旧的一套
      - [x] 拒绝原因走 `HELLO_ACK`（`unknown credential` / `credential expired`）
      - [ ] B（可选，高保障场景）：TLS 客户端证书白名单，凭据不上线
- [x] ~~token 改为 HMAC 挑战-响应~~ → 评估后否决，见 ADR-0004
- [x] `--max-sessions` + 驱逐最久未连接会话 + `RESOURCE_EXHAUSTED` 拒绝（R-3，内存 DoS）
- [x] 每凭据会话配额 `--max-sessions-per-credential`（R-3 余下部分）
- [x] 会话绑定到创建它的凭据身份，杜绝会话接管（R-2）
- [x] 凭据的有效期、标识与撤销（R-5）
- [ ] 握手速率限制与认证失败告警（R-4）
- [x] 自签证书默认有效期改为 825 天并文档化续期（R-6）
- [x] 服务端日志带 `credential=<label>`，部分补偿源 IP 遮蔽（R-1；完整方案仍需接 SIEM）
- [ ] 按客户端身份路由多个 target（白名单 → 策略）
- [ ] 结构化指标（Prometheus：重连次数、重放字节、RTT、窗口阻塞时长）
- [ ] systemd unit / launchd plist / Windows 服务封装
- [ ] 接入 quic-go Path API（`AddPath`/`Probe`/`Switch`）做主动路径迁移，换网时免去一次握手
- [ ] 真机执行 `test/roaming/run.sh`（R1–R7）：Wi-Fi↔蜂窝、飞行模式、NAT 超时，结论归档

## M4 · 跨进程持久化（未开始）
- [ ] 会话状态快照（偏移量 + 未 ACK 缓冲）落盘
- [ ] 客户端 daemon + 短命 CLI（`ProxyCommand` 走 unix socket 接入常驻 daemon）
- [ ] 进程重启后恢复 Session（目标：`SESSION_UNKNOWN` 不再是常态）

## M5 · 性能与扩展（未开始）
- [ ] 启用 TLS 会话恢复（**不开 0-RTT**）以缩短重连握手，衡量对换网卡顿的改善
- [ ] ALPN 可配置（R-11）与 `--no-sni`（R-12），面向受限网络
- [ ] 多 QUIC 流承载（分流重放）以消除帧级排队，benchmark 对比
- [ ] 0-RTT 重连（需评估重放攻击面）
- [ ] TCP 承载兜底（UDP 被封时换一层 Link；会话层已与承载解耦）
- [ ] 评估 multipath QUIC（Wi-Fi + 蜂窝聚合），跟踪 IETF 进展
- [ ] UDP 转发（DNS/QUIC 穿透）与 `-D` SOCKS 场景压测
