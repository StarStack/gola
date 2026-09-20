<p align="center">
  <img src="docs/assets/gola-logo.png" width="140" alt="GoLa 标志">
</p>

<h1 align="center">GoLa Web Framework</h1>

<p align="center">
  <strong>熟悉的 API · 原生 Go HTTP · 专注的核心</strong><br>
  直接基于 Go 标准库 <code>net/http</code> 构建的轻量 HTTP 框架。
</p>

<p align="center">
  <img src="docs/assets/go-version.svg" height="24" alt="Go 1.26 或更高版本">
  <img src="docs/assets/net-http.svg" height="24" alt="基于 net/http">
  <img src="docs/assets/stdlib-only.svg" height="24" alt="核心仅依赖标准库">
  <a href="CHANGELOG.md"><img src="docs/assets/version.svg" height="24" alt="版本：v0.1.0"></a>
</p>

<p align="center">
  <a href="README.md">English</a> · <strong>简体中文</strong>
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#路由与中间件">路由</a> ·
  <a href="#文档导航">文档</a> ·
  <a href="examples/README.md">示例</a> ·
  <a href="docs/security.md">安全</a>
</p>

---

GoLa 是直接基于 Go 标准库 `net/http` 构建的轻量 HTTP 框架。核心仅依赖 Go 标准库，提供路由、中间件、请求绑定和响应助手，适合构建 REST API、Web 服务和微服务。

## 为什么使用 GoLa？

| 特性 | 你会获得 |
| --- | --- |
| **熟悉的路由 API** | 使用 `GET`、`Group` 和 `Use` 组织接口，支持静态路径、命名参数与末尾通配符。 |
| **原生 `net/http`** | Engine 实现 `http.Handler`，也能通过适配器接入已有的标准 Handler。 |
| **可组合的中间件** | 全局、分组和路由级处理链，通过 `Next` 执行前后置逻辑，通过 `Abort` 中断后续处理。 |
| **明确的输入绑定** | 分别绑定 JSON、XML、Query、URI 与 Form，可注入校验器，被绑定的请求体默认上限为 1 MiB。 |
| **文件与渲染** | 有界 Multipart 上传、受控文件保存与下载、HTML 模板、XML 响应与 SSE。 |
| **可选扩展** | OpenAPI、gzip、限流、有界请求指标，以及独立模块中的链路追踪和 WebSocket。 |
| **实用的默认配置** | `Default()` 提供 Request ID、结构化访问日志、Panic 恢复与请求体限制。 |
| **清晰的信任边界** | 默认不信任代理，CORS 需显式配置，不自动重写路径或重定向。 |

配置方式、输入限制与信任边界，见 [安全使用指南](docs/security.md)。

## 快速开始

需要 **Go 1.26 或更高版本**，建议使用受支持系列的最新安全补丁。

### 运行示例

在项目根目录启动：

```sh
go run ./examples/hello
```

保持服务运行，在另一个终端访问：

```sh
curl http://127.0.0.1:8080/hello/Danny
```

```json
{"hello":"Danny"}
```

也可以在浏览器中打开该地址。服务会持续占用终端，按 `Ctrl+C` 停止。

### 第一个 GoLa 服务

```go
package main

import (
	"log"
	"net/http"

	"github.com/starstack/gola"
)

func main() {
	r := gola.Default()

	r.GET("/hello/:name", func(c *gola.Context) {
		if err := c.JSON(http.StatusOK, gola.H{
			"hello": c.Param("name"),
		}); err != nil {
			c.Fail(err)
		}
	})

	if err := r.Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
```

`Default()` 安装 Request ID、访问日志、Recovery 和请求体限制中间件。需要自行组合中间件时使用 `New()`。

### 在本地项目中引用

Module 路径为 `github.com/starstack/gola`。本版本发布前，请通过本地 `replace` 引用 GoLa 源码目录。

如果应用还没有 `go.mod`，先在应用目录执行 `go mod init example.com/myapp`。保存上面的服务代码为 `main.go`，然后运行以下命令，并把绝对路径替换为 GoLa 的实际位置：

```sh
go mod edit -require=github.com/starstack/gola@v0.0.0
go mod edit -replace=github.com/starstack/gola=/absolute/path/to/GoLA
go mod tidy
go run .
```

## 路由与中间件

将相关路由放在同一分组中，共享中间件：

```go
api := r.Group("/api")
api.Use(func(c *gola.Context) {
	c.Header("X-Service", "GoLa")
	c.Next()
})

api.GET("/users/:id", func(c *gola.Context) {
	if err := c.JSON(http.StatusOK, gola.H{"id": c.Param("id")}); err != nil {
		c.Fail(err)
	}
})
```

在服务启动前完成路由和中间件配置。处理器正常返回会继续执行后续链；需要拒绝请求时，调用 `AbortWithStatus` 或 `AbortWithStatusJSON` 后立即 `return`，退出当前处理器。

完整行为见 [分组与认证示例](examples/groups/main.go) 和 [API 参考](docs/api.md)。

## 基于 net/http

GoLa 直接使用标准 `*http.Request` 与 `http.ResponseWriter`。Go 标准库负责 HTTP 协议、TLS、连接管理与请求取消，GoLa 负责路由分派与应用处理链。

通过 `WrapH` 或 `WrapF` 接入已有的标准 Handler：

```go
r.GET("/standard", gola.WrapF(func(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}))
```

需要自行控制服务器配置时，将 `r.Run(...)` 替换为标准 Server。以下片段额外需要导入 `time`：

```go
server := &http.Server{
	Addr:              "127.0.0.1:8080",
	Handler:           r,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       15 * time.Second,
	WriteTimeout:      30 * time.Second,
	IdleTimeout:       60 * time.Second,
	MaxHeaderBytes:    1 << 20,
}

if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
	log.Fatal(err)
}
```

[服务生命周期示例](examples/graceful/main.go) 进一步演示信号处理与优雅退出。超时数值应按应用需求调整。

## 文档导航

中英文 README 提供相同的入门内容。详细指南目前以中文维护，并配有可运行的 Go 示例。

| 文档 | 内容 |
| --- | --- |
| [API 参考](docs/api.md) | 路由、Context、绑定、响应、选项与 CORS |
| [安全使用指南](docs/security.md) | 可信代理、输入限制、日志、跨域与安全更新 |
| [架构说明](docs/architecture.md) | Engine、RouterGroup、Context 与 `net/http` 的关系 |
| [示例目录](examples/README.md) | 可运行的路由、中间件、文件、渲染与流式响应示例 |
| [文件上传](docs/uploads.md) | Multipart 文件与字段、大小限制、临时文件生命周期 |
| [文件保存与下载](docs/files.md) | 受控路径、附件、静态资源与 HTTP Range |
| [HTML 与 XML](docs/rendering.md) | 自动转义模板、XML 响应与有界 XML 绑定 |
| [SSE](docs/streaming.md) / [WebSocket](docs/websocket.md) | 流式响应、取消、限额与连接退出 |
| [可选中间件](docs/middleware.md) | gzip、进程内限流与有界 JSON 指标 |
| [OpenAPI](docs/openapi.md) / [链路追踪](contrib/otel/README.md) | 显式接口描述与可选 OpenTelemetry 适配 |
| [性能基准](docs/benchmarks.md) | 可复现的性能测量、对照方法与原始结果 |
| [贡献指南](CONTRIBUTING.md) | 开发、测试与文档约定 |

## 性能基准

基准覆盖静态路由、参数路由、404、JSON、中间件和不同路由数量。对比测试使用等价的请求与响应，具体对照实现与版本记录在基准文档中。

复现方式及实测结果见 [性能基准](docs/benchmarks.md)。这些数据来自 `ServeHTTP` 微基准，不代表生产吞吐量。对照测试的依赖隔离在独立模块中。

## 维护者

由 **Danny** 与新加坡公司 **STARDATA INTERNATIONAL PTE.LTD.** 维护。

## 致谢

GoLa 基于 Go 标准库 `net/http` 独立实现。
部分 API 设计与开发体验受到 Gin 的启发。

## 许可证

GoLa 基于 [MIT License](LICENSE) 开源。

Copyright (c) 2026 STARDATA INTERNATIONAL PTE.LTD.
