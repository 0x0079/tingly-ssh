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
- [x] CLI：`server` / `client` / `proxy` / `keygen`

## M3 · 生产加固（未开始）
- [ ] token 改为 HMAC 挑战-响应（`04-security-model.md` §3）
- [ ] 按客户端身份路由多个 target（白名单 → 策略）
- [ ] 结构化指标（Prometheus：重连次数、重放字节、RTT、窗口阻塞时长）
- [ ] systemd unit / launchd plist / Windows 服务封装
- [ ] 接入 quic-go Path API（`AddPath`/`Probe`/`Switch`）做主动路径迁移，换网时免去一次握手
- [ ] 真机验收：Wi-Fi↔5G 切换、地铁断网、NAT 超时（`06-testing.md` §4）

## M4 · 跨进程持久化（未开始）
- [ ] 会话状态快照（偏移量 + 未 ACK 缓冲）落盘
- [ ] 客户端 daemon + 短命 CLI（`ProxyCommand` 走 unix socket 接入常驻 daemon）
- [ ] 进程重启后恢复 Session（目标：`SESSION_UNKNOWN` 不再是常态）

## M5 · 性能与扩展（未开始）
- [ ] 多 QUIC 流承载（分流重放）以消除帧级排队，benchmark 对比
- [ ] 0-RTT 重连（需评估重放攻击面）
- [ ] 评估 multipath QUIC（Wi-Fi + 蜂窝聚合），跟踪 IETF 进展
- [ ] UDP 转发（DNS/QUIC 穿透）与 `-D` SOCKS 场景压测
