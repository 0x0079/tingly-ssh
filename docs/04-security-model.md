# 04 · 安全模型

## 1. 首要原则

**SSH 的端到端安全不可被降级。** 隧道是 SSH 之外的一层额外封装：
即使隧道被完全攻破（攻击者成为中间人），攻击者得到的也只是 SSH 密文，
与直接在网络上抓包等价——SSH 的主机密钥校验与用户认证仍然成立。

隧道层安全的目标因此是**可用性与接入控制**，而不是机密性的最后防线：

1. 服务端不能变成任意 TCP 的开放中继（open relay）；
2. 攻击者不能劫持或干扰他人的 Session；
3. 被动观测者不应免费获得连接元数据（谁连了哪个内网端口）。

## 2. 信任边界与机制

| 风险 | 机制 |
| --- | --- |
| 客户端连到假服务端 | TLS 1.3（QUIC 内置）+ **SPKI pin**（`--pin sha256:<base64>`），或系统 CA + `--server-name` |
| 未授权客户端接入 | 预共享 token（`--token-file`），TLS 内传输，服务端 `subtle.ConstantTimeCompare` 比较 |
| 开放中继 | 服务端只连 `--target` 白名单；客户端 OPEN 里的 `target` 只是提示，不在白名单内即 RESET |
| Session 劫持 | `SessionID` 为 16 字节 CSPRNG，且**只能在通过 token 认证的 TLS 连接上使用**；猜中 ID 也需要有效 token |
| 旧链路抢占 / 重放 HELLO | `epoch` 单调递增，过期 HELLO 被拒（`EPOCH_STALE`） |
| 放大攻击 | QUIC 地址验证（quic-go 内置 Retry/token），不自行实现 |
| 资源耗尽 | `--max-streams`、每流窗口上限、Session linger 回收、握手超时 |

## 3. token 的定位（v0 的已知弱点）

v0 的 token 是 **bearer token over TLS**（等价于 HTTP `Authorization: Bearer`）：

- 安全性依赖 TLS 服务端认证正确（因此 **强烈建议使用 `--pin`**）；
- `--insecure` 会跳过证书校验：此时 token 可被中间人窃取。
  该选项仅供本机开发，文档与启动日志都会显式告警。

M3 计划改为挑战-响应：服务端在 `HELLO_ACK` 前发送 nonce，客户端回 `HMAC(token, nonce || session_id || epoch)`，
使 token 本身不再上线。见 `05-roadmap.md`。

## 4. 密钥与证书管理

- 服务端首次启动若无 `--cert`/`--key`，生成 Ed25519 自签名证书并持久化到 `--state-dir`，
  在日志中打印 `pin=sha256:<base64(SHA256(SPKI))>`，供客户端配置。
- token 由 `tingly-shell keygen` 生成（32 字节随机，base64），文件权限要求 `0600`，
  否则服务端拒绝启动（防止复制粘贴导致的全局可读 token）。
- 不做密钥轮转自动化：轮转 = 换文件 + 重启（会话会中断），记录在 roadmap。

## 5. 日志与隐私

- 默认日志不含 token、不含完整 `SessionID`（只打印前 8 hex），不含用户数据内容。
- `--log-level debug` 会打印帧头（类型、stream、offset、长度），不打印 payload。
