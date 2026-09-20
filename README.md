<p align="center">
  <img src="docs/assets/gola-logo.png" width="140" alt="GoLa logo">
</p>

<h1 align="center">GoLa Web Framework</h1>

<p align="center">
  <strong>Familiar APIs. Native Go HTTP. A focused core.</strong><br>
  A lightweight HTTP framework built directly on Go’s <code>net/http</code>.
</p>

<p align="center">
  <img src="docs/assets/go-version.svg" height="24" alt="Go 1.26 or later">
  <img src="docs/assets/net-http.svg" height="24" alt="Built on net/http">
  <img src="docs/assets/stdlib-only.svg" height="24" alt="Core uses only the standard library">
  <a href="CHANGELOG.md"><img src="docs/assets/version.svg" height="24" alt="Version: v0.1.0"></a>
</p>

<p align="center">
  <strong>English</strong> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#routing-and-middleware">Routing</a> ·
  <a href="#documentation">Documentation</a> ·
  <a href="examples/README.md">Examples</a> ·
  <a href="docs/security.md">Security</a>
</p>

---

GoLa is a lightweight HTTP framework built directly on Go’s `net/http`. The core depends only on the Go standard library and provides routing, middleware, request binding, and response helpers for REST APIs, web services, and microservices.

## Why GoLa?

| Feature | What you get |
| --- | --- |
| **Familiar routing** | `GET`, `Group`, and `Use`, with static paths, named parameters, and trailing wildcards. |
| **Native `net/http`** | An Engine that implements `http.Handler`, plus adapters for your existing standard handlers. |
| **Composable middleware** | Global, group, and route chains, with `Next` for before/after logic and `Abort` for stopping the chain. |
| **Explicit input binding** | Separate JSON, XML, query, URI, and form inputs, an optional validator, and a default 1 MiB limit on bound request bodies. |
| **Files and rendering** | Bounded multipart uploads, controlled file storage and downloads, HTML templates, XML responses, and SSE. |
| **Optional extensions** | OpenAPI, gzip, rate limiting, bounded request metrics, and separate modules for tracing and WebSocket. |
| **Practical defaults** | Request IDs, structured access logs, panic recovery, and body limits through `Default()`. |
| **Clear trust boundaries** | No trusted proxies by default, explicit CORS configuration, and no automatic path rewriting or redirects. |

Read the [security guide](docs/security.md) for configuration, input limits, and trust boundaries.

## Quick start

Requires **Go 1.26 or later**. Use the latest security patch of a supported Go release.

### Run the example

From the repository root:

```sh
go run ./examples/hello
```

Keep the service running and open another terminal:

```sh
curl http://127.0.0.1:8080/hello/Danny
```

```json
{"hello":"Danny"}
```

You can also open that URL in a browser. The server keeps the terminal occupied until you stop it with `Ctrl+C`.

### Your first service

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

`Default()` installs Request ID, access logging, recovery, and body-limit middleware. Use `New()` to assemble your own middleware stack.

### Use GoLa in a local project

The module path is `github.com/starstack/gola`. Before this version is published, use a local `replace` directive to reference your GoLa checkout.

In your application directory, run `go mod init example.com/myapp` if you do not already have a `go.mod`. Save the service above as `main.go`, then run these commands, replacing the absolute path with your GoLa checkout:

```sh
go mod edit -require=github.com/starstack/gola@v0.0.0
go mod edit -replace=github.com/starstack/gola=/absolute/path/to/GoLA
go mod tidy
go run .
```

## Routing and middleware

Group related routes and share middleware:

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

Configure routes and middleware before serving requests. Returning from a handler normally advances the chain. To reject a request, call `AbortWithStatus` or `AbortWithStatusJSON` and then `return` from the current handler.

See the [group and authentication example](examples/groups/main.go) and the [API reference](docs/api.md) for the full behavior.

## Built on net/http

GoLa works directly with standard `*http.Request` and `http.ResponseWriter` types. Go’s standard library handles HTTP, TLS, connections, and request cancellation; GoLa handles routing and the application’s handler chain.

Adapt an existing handler with `WrapH` or `WrapF`:

```go
r.GET("/standard", gola.WrapF(func(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}))
```

For control over server settings, replace `r.Run(...)` with a standard server. This snippet also requires the `time` import:

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

The [server lifecycle example](examples/graceful/main.go) adds signal handling and graceful shutdown. Choose timeouts to suit your application.

## Documentation

The two READMEs cover the same getting-started material. Detailed guides are currently available in Chinese; runnable Go examples accompany them.

| Guide | Contents |
| --- | --- |
| [API reference](docs/api.md) | Routes, Context, binding, responses, options, and CORS |
| [Security](docs/security.md) | Trusted proxies, input limits, logging, CORS, and security updates |
| [Architecture](docs/architecture.md) | How Engine, RouterGroup, Context, and `net/http` fit together |
| [Examples](examples/README.md) | Runnable examples of routing, middleware, files, rendering, and streaming |
| [File uploads](docs/uploads.md) | Multipart files and fields, size limits, and temporary-file lifetime |
| [File storage and downloads](docs/files.md) | Controlled paths, attachments, static assets, and HTTP range requests |
| [HTML and XML](docs/rendering.md) | Escaped templates, XML responses, and bounded XML binding |
| [SSE](docs/streaming.md) / [WebSocket](docs/websocket.md) | Streaming, cancellation, limits, and connection shutdown |
| [Middleware](docs/middleware.md) | Opt-in gzip, process-local rate limits, and bounded JSON metrics |
| [OpenAPI](docs/openapi.md) / [Tracing](contrib/otel/README.md) | Explicit API descriptions and an optional OpenTelemetry adapter |
| [Benchmarks](docs/benchmarks.md) | Reproducible performance measurements and comparisons, including raw results |
| [Contributing](CONTRIBUTING.md) | Development, testing, and documentation conventions |

## Benchmarks

The benchmark suite covers static and parameterized routes, 404 responses, JSON, middleware, and varying route counts. Comparisons use equivalent requests and responses; the benchmark guide records the implementations and versions tested.

See the [methodology and measured results](docs/benchmarks.md) to reproduce the comparison. These are `ServeHTTP` microbenchmarks, not production throughput claims. Comparison dependencies are isolated in a separate module.

## Maintainers

Maintained by **Danny** and **STARDATA INTERNATIONAL PTE.LTD.**, Singapore.

## Acknowledgements

GoLa is independently implemented on top of Go's standard `net/http` package.
Parts of its API design and developer experience are inspired by Gin.

## License

GoLa is released under the [MIT License](LICENSE).

Copyright (c) 2026 STARDATA INTERNATIONAL PTE.LTD.
