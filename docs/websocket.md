# WebSocket 集成

GoLa 通过 `WrapH` 接入标准 `http.Handler`。WebSocket 使用独立示例模块中的 [`github.com/coder/websocket`](https://github.com/coder/websocket)，版本固定为 `v1.8.15`；核心框架没有因此引入第三方依赖。库的协议升级通过 `Unwrap` 找到底层 `http.Hijacker`，示例使用 HTTP/1.1 WebSocket 升级。

## 运行

独立模块需要进入自己的目录运行，首次运行会下载固定依赖：

```bash
cd examples/websocket
go run .
```

服务监听 `127.0.0.1:8080`，端点为 `ws://127.0.0.1:8080/ws`。浏览器先打开 `http://127.0.0.1:8080/`，再在该页面的开发者控制台执行：

```javascript
const socket = new WebSocket("ws://127.0.0.1:8080/ws");
socket.onopen = () => socket.send("Hello Danny");
socket.onmessage = (event) => console.log(event.data);
socket.onclose = (event) => console.log("closed", event.code);
```

服务回显文本和二进制消息。结束客户端使用 `socket.close()`，结束服务在终端按 Ctrl+C。

## 输入与连接限制

- 单条消息最多 64 KiB，包括分片消息；超限关闭码为 `1009`。
- 同时最多 128 个处于握手或已连接状态的处理函数；达到限制或进入关闭状态后，新连接返回 HTTP 503。
- 每次读取最多等待 60 秒，每次写入最多等待 5 秒；空闲超时或传输失败结束连接。
- 压缩关闭，避免示例引入解压缩资源开销；外层普通 HTTP gzip 中间件仍可使用，升级响应不会压缩。
- Origin 存在时，必须恰好一个合法来源，与当前请求的 scheme、host 和 port 一致。拒绝跨来源、`null`、路径、用户信息和重复 Origin。没有 Origin 的非浏览器客户端可以连接。

Origin 检查不是身份鉴别。该示例只在本机回显消息，没有用户系统；业务服务应在升级之前加入自己的认证与授权。TLS 终止代理后的公开 Origin 必须通过应用明确配置的来源策略处理，不能直接信任请求中的任意转发头，也不要打开无条件跨来源。

## 生命周期

HTTP 连接升级后，由应用维护独立的上下文和连接登记。示例不保留 GoLa `Context`，不把原 HTTP 请求上下文当成长连接生命周期。处理函数退出时移除登记、取消上下文并关闭连接。

`http.Server.Shutdown` 不会关闭或等待已 hijack 的 WebSocket 连接。示例在退出时停止接收新握手，向登记的连接发送固定 `1001` 关闭信息，同时等待 HTTP 服务和 WebSocket 处理函数退出；客户端不响应关闭握手时，关闭期限触发取消并清理连接。处理函数发生 panic 时仅返回固定 `1011` 与 `internal error`，不把 panic 内容、请求内容或内部错误发送给客户端。

这里展示框架互通和回显服务的完整生命周期；订阅、房间、广播、消息重放和持久化由业务层实现。标准库关闭契约见 [`http.Server.Shutdown`](https://pkg.go.dev/net/http#Server.Shutdown)，读写和关闭行为见 [WebSocket API](https://pkg.go.dev/github.com/coder/websocket)。

## 独立验证

```bash
cd examples/websocket
go test -race ./...
```

测试覆盖文本与二进制往返、外层 gzip 中间件、Origin 拒绝、消息限额、panic 内容隐藏、服务停止时活跃连接关闭以及不响应关闭的客户端清理。完整示例见 [main.go](../examples/websocket/main.go)。
