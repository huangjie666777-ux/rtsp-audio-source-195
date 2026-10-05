# RTSP 1.0 PCMU Audio Server (Go 1.27.1 + Pion RTP 1.8.25)

一个用于设备联调的 RTSP 1.0 over TCP 后端，把 `assets/demo.ulaw`
（8kHz 单声道 PCMU，时长 8.0s）以 20ms / 160 样本一包的节奏通过
RTP/AVP/TCP 交织帧推送。

## 构建与启动

```sh
go build -o rtsp-server .          # 构建服务器
go build -o demo-client ./cmd/demo # 构建演示客户端
./rtsp-server -port 8554 -file assets/demo.ulaw
```

- `-port`：本机 TCP 监听端口（默认 8554）
- `-file`：PCMU 裸流文件（默认 `assets/demo.ulaw`）

资源地址：`rtsp://127.0.0.1:<port>/demo`，单轨 `trackID=0`。

## 演示客户端

```sh
./demo-client -addr 127.0.0.1:8554
```

依次执行 OPTIONS → DESCRIBE → SETUP → PLAY → PAUSE → 续播 →
`Range: npt=2.0-` 定位 → TEARDOWN，并统计实际收到的 RTP 包数、
序号与时间戳，验证播放、暂停（暂停期间 0 包）与定位。

## 协议要点

- 方法：OPTIONS、DESCRIBE、SETUP、PLAY、PAUSE、TEARDOWN；SDP 声明载荷
  类型 0（PCMU/8000/1）与 `a=range` 总时长。
- 仅支持 `RTP/AVP/TCP`，使用客户端指定的两个不同 interleaved 通道
  （RTP 偶通道、RTCP 奇通道），客户端发来的 `$` RTCP 帧被跳过。
- 请求按 CRLF / Content-Length / CSeq 解析，支持半包与连续（流水线）
  命令；请求行 ≤1KB、头部 ≤8KB、正文 ≤64KB；响应回显 CSeq。
- SETUP 创建连接独占的随机 Session；错误 Session 返回 454，错误状态
  返回 455，播放中拒绝重复 PLAY。
- PLAY 无 Range 时从暂停位置续播；`Range: npt=x-` 要求 x 有限、非负且
  小于时长，按 20ms 向下对齐，响应携带实际起点 `Range` 与
  `RTP-Info`（seq/rtptime）。
- RTP v2，PT=0，随机独立 SSRC；序号在会话内连续，时间戳按已发样本
  递增，定位只移动文件游标。响应与 `$` 帧在同一把写锁下串行写出，
  首包保证在 PLAY 响应之后。
- PAUSE 保留下一样本位置，响应之后不再发包，续播无跳漏；文件末尾自动
  暂停。TEARDOWN、断连或写入超时（5s）都会停止发送并释放状态，各
  客户端互不影响。

## 代码结构

- `main.go`：命令行入口（端口、媒体文件）
- `server.go`：TCP 监听、媒体加载、SDP 生成
- `rtsp.go`：RTSP 请求/响应解析（CRLF、Content-Length、`$` 帧、长度限制）
- `session.go`：方法分发、会话状态机、Transport/Range 解析
- `media.go`：20ms 节拍器与 Pion RTP 打包、交织帧发送
- `cmd/demo/main.go`：TCP 演示客户端

## 测试

```sh
go test ./...
```

覆盖连续命令解析、`$` 帧跳过、长度限制、Transport 与 Range 校验。
