# Server-Sent Events

`c.SSEvent(name, value)` 向当前请求写入并刷新一个事件。数据使用 JSON 编码，客户端应对 `event.data` 调用 `JSON.parse`。名称为空时使用浏览器默认的 `message` 事件；有名称时使用 `addEventListener(name, handler)`。

```go
r.GET("/events", func(c *gola.Context) {
    if err := c.SSEvent("ready", gola.H{"status": "connected"}); err != nil {
        c.Fail(err)
        return
    }
    // A real subscription waits for data or c.Request.Context().Done().
})
```

第一帧提交 HTTP 200，设置 `Content-Type: text/event-stream; charset=utf-8`，删除固定 `Content-Length`。没有配置 Cache-Control 时默认设置 `no-cache`，已有的 `private, no-store` 等应用策略保留。同一处理函数可重复发送事件；不能附加到已经提交的普通响应，也不能在更改流的 Content-Type 后继续发送。HEAD 请求返回 `http.ErrBodyNotAllowed`。

事件名称包含 CR、LF、NUL 或非法 UTF-8 时返回 `ErrInvalidSSEEvent`。JSON 编码避免数据中的换行插入额外事件。编码错误和底层未提供刷新接口的错误在首帧提交之前返回；未提供刷新接口时返回 `http.ErrNotSupported`。接口存在但实际刷新失败的错误可能在响应提交后返回。底层通过 `FlushError`、`http.Flusher` 或 `Unwrap` 链提供刷新能力，并由标准 `http.ResponseController` 完成刷新。

## 连接生命周期

在请求的处理 goroutine 中写响应；不要把 `Context` 交给后台 goroutine。应用负责订阅、身份鉴别、连接数限制和事件来源；`SSEvent` 不自动创建后台任务，也不提供重连事件 ID、历史重放或持久化队列。浏览器的自动重连行为见 [SSE 标准](https://html.spec.whatwg.org/multipage/server-sent-events.html)。

长连接使用显式 `http.Server` 配置。`Engine.Run` 的普通 API 写超时为 30 秒，不适合无限期 SSE。示例为长流取消整个响应的 `WriteTimeout`，并在每次写入前使用 `ResponseController.SetWriteDeadline` 设置 5 秒期限，避免慢客户端阻塞写入。成功刷新后清除期限，防止它在两帧之间的等待期过期；下一帧前重新设置期限。示例每 15 秒发送一次 JSON 心跳事件。

每轮等待应同时监听 `c.Request.Context().Done()`，遇到写入或刷新错误立即结束。关闭进程时应取消长流的请求上下文，再调用 `Server.Shutdown`；否则长期活跃的流会阻止排空。示例停止 ticker，等待服务退出，并在关闭超时后关闭连接。反向代理还需要配置关闭响应缓冲并允许足够的连接空闲时间。

## 运行示例

```bash
go run ./examples/sse
```

另开终端持续接收：

```bash
curl -N http://127.0.0.1:8080/events
```

首先返回 `ready`，随后每 15 秒返回一次 `heartbeat`。在客户端按 Ctrl+C 会取消请求；在服务终端按 Ctrl+C 会结束活跃流并关闭服务器。完整代码见 [examples/sse](../examples/sse/main.go)。
