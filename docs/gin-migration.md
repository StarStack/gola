# Gin 结构参考与迁移

GoLa 参考 Gin 的结构和使用体验，运行时直接建立在 Go 标准库 `net/http` 上。Engine 自己实现 `http.Handler`；没有把 Gin 包装为运行时内核，也没有自研 HTTP 协议栈。Gin 仅出现在独立的基准模块中。

## 固定参考

参考基线为 [Gin v1.12.0 发布页](https://github.com/gin-gonic/gin/releases/tag/v1.12.0)，对应发布提交为 `73726dc`。以下固定 tag 的源码和测试是结构依据，而不是浮动 master：

| Gin 源文件/测试 | 核对内容 | GoLa 对应职责 |
| --- | --- | --- |
| [gin.go](https://github.com/gin-gonic/gin/blob/v1.12.0/gin.go) | Engine、ServeHTTP、方法树分派及默认处理链 | `engine.go` 负责构造、冻结、请求分派与标准库接入 |
| [routergroup.go](https://github.com/gin-gonic/gin/blob/v1.12.0/routergroup.go) 和 [routergroup_test.go](https://github.com/gin-gonic/gin/blob/v1.12.0/routergroup_test.go) | 分组前缀、注册方法、链组合 | `routergroup.go` 负责组层级与注册；`router.go` 使用路径段树 |
| [context.go](https://github.com/gin-gonic/gin/blob/v1.12.0/context.go) 和 [context_test.go](https://github.com/gin-gonic/gin/blob/v1.12.0/context_test.go) | Next 自动推进、Abort 只终止尚未执行链、请求参数 | `context.go` 的同名入口与请求局部状态 |
| [response_writer.go](https://github.com/gin-gonic/gin/blob/v1.12.0/response_writer.go) | 响应状态和字节计数 | `response.go` 包装标准 ResponseWriter，以 Unwrap/ResponseController 访问可选能力 |

Gin `handleHTTPRequest` 调用 `c.Next()` 驱动整条链；`Next` 在处理器返回后继续循环。所以“不调用 Next”并不等于拒绝请求。GoLa 保持这个可观察行为，但使用独立实现。GoLa 核心独立实现，未复制 Gin 源码。

## 常见 API 对照

| Gin | GoLa | 迁移注意点 |
| --- | --- | --- |
| `gin.New()` / `gin.Default()` | `gola.New()` / `gola.Default(opts...)` | GoLa Default 顺序为 Request ID、访问日志、Recovery、BodyLimit |
| `gin.HandlerFunc` | `gola.HandlerFunc` | 都是 Context 回调风格，但 Context 类型不同，需要改写或显式适配 |
| `GET` / `Group` / `Use` | 同名方法 | GoLa 注册方法不返回可继续链式调用的接口 |
| `Next` / `Abort` | 同名方法 | 鉴权失败 `AbortWithStatus...` 后仍要 `return` |
| `Param` / `Query` / `FullPath` | 同名方法 | GoLa `*path` 参数没有前导 `/`；权限可使用匹配模板 |
| `ShouldBindJSON` / `ShouldBindQuery` | 同名方法 | GoLa URI 入口为 `ShouldBindURI`；Form 只读 URL 编码 Body |
| `JSON` / `String` / `Data` / `Redirect` | 同名方法，返回 `error` | 调用方必须处理序列化、提交后调用和断连写错误 |
| `c.Errors` / `c.Error(err)` | `c.Errors()` / `c.Error(err)` | GoLa 只读副本；`Fail` 用于记录、Abort 并交给 ErrorHandler |
| `gin.WrapH` / `gin.WrapF` | `gola.WrapH` / `gola.WrapF` | GoLa 把标准 Handler 当终点，返回后中断待执行链 |
| `SetTrustedProxies` | 同名方法 | GoLa 默认不信任任何转发头，必须明确列出代理 |
| `Run(addr...)` | `Run(addr string)` | GoLa 必须指定地址，使用文档约定的 HTTP 超时；生命周期控制直接配置标准 `http.Server` |
| `c.Writer.(http.Flusher)` 等 | `http.NewResponseController(c.Writer)` | GoLa 不虚假声明底层未支持的 Flusher/Hijacker；可通过 Unwrap 访问底层 |

## 必须逐项确认的行为差异

GoLa 的全局和分组 `Use` 在首次请求之前对整个作用域生效，即使路由已注册。Gin v1.12.0 的 `Group` 与 `handle` 会在创建或注册时组合处理链。迁移后仍应集中完成初始化，首次请求后所有路由、中间件和代理配置都会被冻结，变更显式失败。

GoLa 默认不补斜杠、不改大小写、不合并重复斜杠、不清理点路径。路由只使用 `Request.URL.Path`，参数不再次 URL 解码。`/users` 与 `/users/` 不等价；`/assets/*path` 匹配 `/assets/`，不自动匹配 `/assets`。静态、参数和通配符按优先级完整匹配，并在高优先级分支失败时回退。相同结构的不同参数名属于冲突。

GoLa 总是区分 404 与 405，并在 405 输出 Allow。HEAD 和 OPTIONS 都要显式注册；HEAD、204、304 不输出正文。全局中间件覆盖 404、405 和 CORS 预检；未匹配请求不会误入业务分组链。

GoLa ShouldBind 只返回错误，不写响应。JSON 要求 `application/json` 或 `application/*+json`，只允许一个 JSON 值；JSON、XML、Form 和 Multipart 默认受 1 MiB 实际读取限制，`New` 也不例外。Form 不混入 Query。没有默认第三方字段校验器；只有显式注入 Validator 才执行校验。GoLa 提供 Multipart、HTML 模板、XML 和 SSE，但不承诺兼容 Gin 的全部绑定标签或编码器，具体契约见 [API 参考](api.md)。

GoLa JSON 先完成序列化再提交响应。`Status` 立即提交；完整响应助手重复调用返回 `ErrResponseCommitted`。迁移时先设置 Header，再调用输出助手。已提交后发生 panic 会通过标准 HTTP 中止机制结束响应，不给截断响应补写一份 JSON 错误。

GoLa Context 仅限当前请求期间，不承诺并发修改安全，不提供 Gin Context.Copy 的兼容入口。后台任务应复制业务数据并建立自己的标准 Context。服务层接收 `c.Request.Context()`，不接收框架 Context。

## 鉴权中间件改写示例

```go
func auth(c *gola.Context) {
    if !verifyRequest(c.Request) { // 应用实现真正的身份校验
        if err := c.AbortWithStatusJSON(401, map[string]string{
            "error": "authentication required",
        }); err != nil {
            c.Error(err)
        }
        return
    }
    c.Next()
}
```

完整可运行的分组示例在 `examples/groups`，只使用公开的内存测试凭据。实际应用的身份校验和资源访问授权由应用实现。代理、输入和响应边界见 [安全使用指南](security.md)。
