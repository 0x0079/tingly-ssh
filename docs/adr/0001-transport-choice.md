# ADR-0001 · 传输层选型：QUIC bridge

- 状态：**已接受**
- 日期：2026-09-12

## 背景

SSH 需要可靠有序字节流。要获得漫游能力，候选路线有四条。

## 候选方案

### A. MPTCP（RFC 8684，Standards Track）
保留 TCP 语义，一条"TCP 连接"跨多条 subflow，Wi-Fi/蜂窝切换正是其设计目标。
向上层仍暴露可靠字节流，**对 SSH 零侵入**，是标准答案。

- 优点：标准化程度最高；内核实现；无需任何用户态代理；可多路径聚合。
- 缺点：**部署依赖两端 OS**。Linux ≥5.6 有实现但默认不开；macOS 的 MPTCP 仅对
  特定 API（Siri 类）开放，通用 socket 不可用；Windows 无。服务端不支持时**静默退化**
  为普通 TCP，用户拿不到任何漫游能力却以为有。对"自己做跨平台产品"是致命的。

### B. QUIC bridge（RFC 9000 + 两端用户态 bridge）
QUIC 原生定义 Connection Migration：连接标识是 Connection ID 而非四元组，
客户端 IP/端口变化（Wi-Fi→蜂窝、NAT rebinding）可继续同一连接。

- 优点：纯用户态，跨平台一致；`quic-go` 成熟；加密、拥塞控制、丢包恢复、路径验证全部免费；
  UDP/443 易穿墙；不需要任何内核/发行版配合。
- 缺点：多一跳用户态拷贝；QUIC 的迁移**不等于**"断网十分钟后还能恢复"，需要自己加会话层
  （见 ADR-0002）；UDP 在少数网络被封（需要 TCP fallback，列入 roadmap）。

### C. Transport Converter（RFC 8803）
正式描述了"一端支持新传输、另一端是 legacy TCP endpoint 时用 converter/proxy 做兼容"
的架构，文中直接以 MPTCP + WLAN/蜂窝切换为典型场景。

- 这不是与 B 竞争的方案，而是 **B 的架构背书**：我们的一对 bridge 在概念上就是一对 converter。
  它让"不动 ssh/sshd，在中间做桥接"成为有 IETF 正式架构依据的做法，而非偏门 hack。

### D. 魔改 SSH 跑在 QUIC 上
改 OpenSSH 传输层或实现自己的 SSH 栈。

- 缺点：放弃 OpenSSH 的安全审计与生态（agent、证书、sftp、配置），维护成本极高，
  用户必须换 client 和 server。直接否决。

## 决策

选 **B（QUIC bridge）**，架构叙事引用 **C（RFC 8803）**。
保留 A 作为"用户自己的内核/网络已支持 MPTCP"时的替代建议，但不作为产品路线。

落地形态：

```
OpenSSH → TCP(localhost) → Resumable Session Layer → QUIC → Resumable Session Layer → TCP(localhost) → sshd
```

## 结论取舍

| 目标 | 推荐 |
| --- | --- |
| 在自己可控的内核/网络里解决 | MPTCP |
| 做跨平台独立产品（本项目） | **QUIC bridge** |
| 研究标准化 proxy 架构 | Transport Converter / RFC 8803 |
| 直接改 SSH 跑 QUIC | 不采用 |
