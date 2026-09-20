# 架构说明

GoLa 直接建立在 Go 标准库 `net/http` 上，核心独立实现，按引擎、路由、请求上下文、绑定、响应和中间件划分职责。

## 请求流程

```text
http.Server
    → Engine.ServeHTTP
    → 按 HTTP 方法和 URL.Path 匹配路由
    → 全局中间件 → 分组中间件 → 路由处理器
    → 标准 http.ResponseWriter
```

Engine 实现 `http.Handler`。应用可以使用 `Run` 启动服务，也可以把 Engine 作为自定义 `http.Server.Handler`。标准库负责 HTTP 解析、TLS、连接、取消和 Shutdown；GoLa 负责 HTTP 应用层的请求分派。

## 代码组织

| 文件 | 职责 |
| --- | --- |
| engine.go | New/Default、配置、ServeHTTP 与服务启动 |
| router.go | 按方法组织的路径段树、匹配和优先级回退 |
| routergroup.go | 路径前缀、组继承与中间件注册 |
| context.go | 请求局部状态、参数与处理链 |
| response.go、errors.go | 标准响应包装、输出助手与错误入口 |
| binding.go | 独立数据源绑定与 Validator 接口 |
| multipart.go | 有界 Multipart 解析、文件读取入口与临时文件清理 |
| files.go、files_open_*.go | 基于 os.Root 的受控文件保存、下载与静态资源 |
| render.go、sse.go | HTML/XML 渲染、XML 绑定与 SSE 帧 |
| middleware.go、proxy.go | 基础中间件、可信代理处理 |
| middleware/ | 可选 CORS、gzip、限流与有界指标 |
| openapi/ | 路由与显式元数据生成不可变 OpenAPI 文档 |

## 路由与配置生命周期

路径段树按静态、非空参数、末尾通配符的顺序尝试完整匹配；高优先级分支失败时回退。参数名称属于具体路由模板。路径使用标准库解析后的 `URL.Path`，不重复解码或隐式规范化。

Engine 嵌入根 RouterGroup。分组保留父级引用，首次请求时用 `sync.Once` 构建处理链并冻结配置，因此启动前添加的 Use 能覆盖同一作用域内已注册的路由。配置阶段要求串行执行，冻结后不支持增删路由。

## 请求上下文与响应

每个请求独立创建 Context，不共享可变请求状态。Context 不可跨请求保留或并发修改；取消和服务层数据传递使用标准 `context.Context`。

处理器返回后自动推进；Next 立即执行剩余链，Abort 阻止尚未执行的节点。标准 Handler 通过 WrapH/WrapF 作为终点接入，路径参数同步到 `Request.PathValue`。

响应包装记录状态和正文大小。JSON 在提交前完成编码，完整响应助手拒绝重复提交；原始 Writer 仍支持连续写入。通过 `Unwrap` 与 `http.ResponseController` 使用底层连接能力。

## 依赖边界

根模块只依赖 Go 标准库。`benchmarks/`、`contrib/otel/`、`examples/websocket/` 分别使用独立模块，第三方依赖不进入核心运行时。根目录的 `go test ./...` 不遍历这些嵌套模块，需要分别验证。性能对照实现、版本与方法见 [性能基准](benchmarks.md)。

应用通过中间件、Validator、ErrorHandler 和标准 Context 接入身份校验、资源访问控制、日志和自定义组件。核心负责 HTTP 请求处理，应用管理自己的业务状态与资源生命周期。

具体行为见 [API 参考](api.md) 和 [安全使用指南](security.md)。
