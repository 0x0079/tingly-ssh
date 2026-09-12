# 06 · 测试策略

## 1. 分层

| 层 | 位置 | 覆盖 |
| --- | --- | --- |
| 单元：帧编解码 | `internal/proto/frame_test.go` | round-trip、截断输入、超长帧、非法 varint |
| 单元：会话层 | `internal/mux/session_test.go` | 不变量 I1–I5、FIN/RESET、窗口阻塞、epoch 抢占 |
| 集成：故障注入 | `internal/mux/session_test.go` | 用内存管道模拟 Link，在任意字节位置切断并重连 |
| 端到端：真 QUIC | `internal/bridge/bridge_test.go` | TCP→QUIC→TCP 回环；强制杀 QUIC 连接后恢复 |

## 2. 故障注入手法

会话层测试不依赖真实网络：Link 只要求 `io.ReadWriteCloser`。测试用
`net.Pipe()` 构造 Link，并在传输中途 `Close()` 一侧，验证：

- 写入方不报错（阻塞在窗口或缓冲区）；
- 重连后接收方得到的字节流与写入方写入的完全一致（`bytes.Equal` 全量比对）；
- 接收方不会看到任何重复或空洞（通过递增计数 payload 校验）。

## 3. 本地端到端手工验证

```bash
go build ./cmd/tingly-shell
./tingly-shell keygen > /tmp/token
./tingly-shell server --listen 127.0.0.1:7443 --target 127.0.0.1:22 \
    --token-file /tmp/token --state-dir /tmp/tingly-server   # 日志里抄下 pin=
./tingly-shell client --server 127.0.0.1:7443 --listen 127.0.0.1:2222 \
    --token-file /tmp/token --pin sha256:...
ssh -p 2222 user@127.0.0.1
```

## 4. 真机漫游验收（M3 清单）

| 场景 | 操作 | 期望 |
| --- | --- | --- |
| 换网 | SSH 里跑 `ping -i0.2`，关 Wi-Fi 走蜂窝 | 输出停顿 < 2s 后继续，不断连（QUIC 迁移） |
| 断网恢复 | 飞行模式 40s 后恢复 | 会话继续，日志显示一次 reconnect + replay 字节数 |
| 超过 linger | 飞行模式 90s（linger 60s） | SSH 明确断开，不挂死 |
| NAT 超时 | 空闲 10 分钟（keepalive 关闭时） | 开启 keepalive 后不断连 |
| 大文件 | `scp` 1GB 中途切网 | 传输继续，校验 sha256 一致 |
