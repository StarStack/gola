# 可选 HTTP 中间件

GoLa 的压缩、限流和指标均由应用显式安装，使用 Go 标准库，不自动开启服务或连接外部系统。

## 安装顺序

以下代码展示配置位置；完整可运行示例见 [examples/middleware](../examples/middleware/main.go)。

```go
collector, err := metrics.New(metrics.Config{MaxSeries: 1024})
if err != nil {
    log.Fatal(err)
}
limit, err := ratelimit.New(ratelimit.Config{
    Rate: 10, Burst: 20, MaxKeys: 10000, IdleTimeout: 10 * time.Minute,
})
if err != nil {
    log.Fatal(err)
}

r := gola.New()
r.Use(collector.Middleware(), gola.RequestID(), gola.Recovery(), gola.BodyLimit(8<<20), limit)
r.GET("/hello/:name", func(c *gola.Context) {
    _ = c.JSON(http.StatusOK, gola.H{"hello": c.Param("name")})
})
```

指标放在 Recovery 之前，才能在异常恢复完成后读取最终状态；放在限流之前，才能统计 429。使用 `gola.Default()` 后追加的中间件位于默认 Recovery 之内，需要自定义顺序时使用 `gola.New()`。

## Gzip 压缩

```go
gzipMiddleware, err := compress.New(compress.Config{})
if err != nil {
    log.Fatal(err)
}
server := &http.Server{
    Addr: ":8080",
    Handler: gzipMiddleware(r),
    ReadHeaderTimeout: 5 * time.Second,
}
log.Fatal(server.ListenAndServe())
```

`compress.New` 返回标准 `func(http.Handler) http.Handler`，可包裹整个 Engine 或单个标准 Handler。只为允许压缩的公开内容启用；会同时包含秘密和用户可控内容的动态响应，例如 CSRF token 与用户输入，不应启用压缩。可按路径在标准 `http.ServeMux` 中分别挂载。

- `Config.Level` 为零时使用标准库默认级别；支持 `gzip.HuffmanOnly`、`gzip.DefaultCompression` 和 1–9。它不表示关闭压缩，关闭时不安装中间件。
- 使用流式 gzip，不缓存完整响应、不设置最小响应大小。很小的响应可能变大。
- 解析多个 `Accept-Encoding` 字段、大小写、通配符和合法 `q` 值。`gzip;q=0` 优先于通配符；重复 gzip 声明取最低质量值，非法 gzip 参数按拒绝处理。不主动压缩未声明接受编码的请求。
- 增加 `Vary: Accept-Encoding`，保留已有 Vary。压缩时移除原 `Content-Length`、`Content-MD5`、`Content-Digest`、`Accept-Ranges`，将强 ETag 转为弱 ETag。
- HEAD、CONNECT、Range、连接升级请求以及 101、204、205、206、304 响应不压缩。已有 `Content-Encoding` / `Content-Range`、SSE 的 `text/event-stream`、`Cache-Control: no-transform` 或声明内容摘要 Trailer 的响应也不压缩。
- 首次 `Write` 可探测原始内容类型；显式 `WriteHeader` 时尚未设置 `Content-Type` 的响应保持原样，由 `net/http` 处理内容类型。流式响应应先设置内容类型。
- `http.NewResponseController(w).Flush()` 同时刷新 gzip 和底层传输；不支持的传输返回 `http.ErrNotSupported`。`Unwrap` 保留 ResponseController 对截止时间、全双工和连接升级的访问。也支持标准 `http.Flusher`，需要处理刷新错误时使用 ResponseController。
- Handler panic 时不补写 gzip 结束帧，不改变原始 panic；完成响应时遇到 gzip 收尾写入错误则以 `http.ErrAbortHandler` 中止传输。

此中间件负责 gzip 内容编码，不替应用实现完整的 406 内容协商策略。若应用要求客户端必须接受某种表示，请在业务入口单独校验。

## 请求限流

`ratelimit.New(Config)` 返回 `gola.HandlerFunc`。每个身份使用令牌桶，每次请求消耗一个令牌，`Rate` 为每秒补充令牌数，`Burst` 为初始和最大容量，两者必须显式配置。`Burst` 上限为 1,000,000。

默认身份来自 `Context.ClientIP()`，遵守可信代理配置，不直接相信请求的 `X-Forwarded-For`。需要按登录用户限制时，可在认证中间件之后安装限流，并设置并发安全的 `Key func(*gola.Context) string`。

`MaxKeys` 默认 10,000，上限 1,000,000。`IdleTimeout` 默认十分钟，上限 24 小时，且必须不少于 `Burst / Rate` 的完整补充时间，防止空闲过期提前恢复额度。过期项在后续请求到达时清理，不启动后台 goroutine。容量已满时拒绝新身份，不驱逐活跃身份来恢复额度。

空身份及超过 256 字节的身份共享一个固定的 fallback 桶，也占用容量。默认无法解析的客户端地址因此仍受到限流。拒绝时返回 429、向上取整且至少为一秒的 `Retry-After` 和 `Cache-Control: no-store`，停止后续处理。

额度只在当前中间件实例及进程内共享；多个副本不会自动共享额度。需要全局配额时，应在网关或独立存储适配器中实现。

## 基础请求指标

`metrics.New(Config)` 返回并发安全的 `*Collector`：

| 方法 | 用途 |
| --- | --- |
| `Middleware()` | 统计 GoLa 请求 |
| `Snapshot()` | 返回当前指标的独立副本 |
| `ServeHTTP(w, r)` | 通过 GET / HEAD 导出 JSON，不自动注册路由 |

`Snapshot` 包含 `in_flight` 和 `series`。每组统计包含路由模板 `route`、固定集合中的 `method`、响应 `status`、累计 `requests`、累计 `duration_seconds`、累计 `response_bytes` 和逃出下游处理的 `panics`。耗时包含该中间件之后的处理时间；字节数是 GoLa 接受的应用字节数，位于外层压缩之前。连接升级后的协议字节不在统计范围内。

例如 `/users/123?token=...` 归到 `/users/:id`，不记录原始路径、查询参数或用户身份。未匹配的 404、405 分别使用 `_not_found`、`_method_not_allowed`，其他未匹配响应使用 `_unmatched`；非常见方法统一为 `OTHER`。

`MaxSeries` 默认 1,024，上限 100,000；超过这个标签组合数量的记录归入一个额外的 `_overflow` 桶，它的 `status: 0` 不表示真实响应状态。导出按路由、方法、状态稳定排序。正常桶的状态零表示尚未提交响应就有 panic 逃出；已被下游 Recovery 捕获的异常表现为最终状态 500，不再计入逃出的 panic 数。

JSON 使用标准编码器转义，不提供 Prometheus 文本或百分位估计。可用累计耗时除以请求数计算平均耗时，或将快照交给应用已有的采集器。指标出口应放在经过认证的管理路由，或单独监听的管理服务中；示例只将管理服务绑定到本机。

协议行为以 [HTTP Semantics](https://www.rfc-editor.org/rfc/rfc9110.html) 和标准库 [ResponseController](https://pkg.go.dev/net/http#ResponseController)、[gzip](https://pkg.go.dev/compress/gzip) 为依据。
