# OpenTelemetry 适配

本独立模块提供 `golaotel.New(Config)` 中间件，将请求 span 放入标准 `c.Request.Context()`，供后续请求处理和外部 HTTP 调用继续创建子 span。GoLa 核心不依赖 OpenTelemetry。

```go
middleware, err := golaotel.New(golaotel.Config{
    TracerProvider: provider,
})
if err != nil {
    return err
}
r.Use(middleware)
```

`provider` 由应用使用 OpenTelemetry SDK 构造；exporter、采样策略、资源属性以及 provider 的 Shutdown 均由应用管理。适配器不创建后台 exporter 或修改全局 provider。

默认不提取客户端链路头。可信入口需要接受上游 traceparent 时，显式设置 `Propagator: propagation.TraceContext{}`；应用应决定远端采样标志是否可信。这里不默认传播 Baggage。

Span 名称使用方法加路由模板；未匹配路径使用固定 unmatched，非标准方法归为 `_OTHER`。记录路由模板、方法和响应状态，不记录原始 URL、Query、Authorization、Cookie、Body、原始业务错误或 panic 内容。所有路径都结束 span，panic 保持原有传播及 Recovery 行为。

请求内的 trace context 在下游处理结束后仍然可见，适配器不会恢复旧 Request 而丢弃下游安装的 BodyLimit、请求上下文或其他字段。外层中间件在 `c.Next()` 返回后读取请求体时仍受到已安装的大小限制；请求 span 此时已经结束，若需观测外层后置处理，应将适配器安装在该中间件之前。

测试使用 SDK 内存 exporter，不需要外部观测服务：在本目录运行 `go test ./...`、`go test -race ./...`、`go vet ./...`。根目录的 `go test ./...` 不包含此独立模块。

参考：[OpenTelemetry Go instrumentation](https://opentelemetry.io/docs/languages/go/instrumentation/)。
