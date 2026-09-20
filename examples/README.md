# 可运行示例

下表中的程序属于根模块，只依赖 GoLa 和 Go 标准库，`go test ./...` 会编译这些示例并执行对应测试。程序默认绑定 `127.0.0.1:8080`，请一次运行一个；中间件示例另在本机 9090 端口提供指标。

| 示例 | 启动命令 | 请求 |
| --- | --- | --- |
| hello | `go run ./examples/hello` | `curl http://127.0.0.1:8080/hello/Danny` |
| 分组与认证中断 | `go run ./examples/groups` | `curl -H 'Authorization: Bearer demo-only-token' http://127.0.0.1:8080/api/v1/me` |
| 绑定与校验 | `go run ./examples/binding` | `curl -H 'Content-Type: application/json' -d '{"name":"Danny"}' http://127.0.0.1:8080/users` |
| 文件上传 | `go run ./examples/upload` | `curl -F 'file=@README.md' -F 'description=GoLa example' http://127.0.0.1:8080/upload` |
| 文件保存与下载 | `go run ./examples/files` | `curl -F 'file=@README.md' http://127.0.0.1:8080/upload`，按返回地址下载 |
| HTML/XML | `go run ./examples/render` | `curl 'http://127.0.0.1:8080/hello?name=Danny'` |
| SSE | `go run ./examples/sse` | `curl -N http://127.0.0.1:8080/events` |
| gzip、限流与指标 | `go run ./examples/middleware` | `curl --compressed http://127.0.0.1:8080/hello/Danny`；`curl http://127.0.0.1:9090/` |
| OpenAPI | `go run ./examples/openapi` | `curl http://127.0.0.1:8080/openapi.json` |
| 优雅退出 | `go run ./examples/graceful` | `curl http://127.0.0.1:8080/work`，在请求进行中向服务进程发送 SIGTERM |

认证示例使用公开的内存测试凭据，演示分组中间件、`Abort` 后 `return` 和类型化标准 Context 传递。没有提供凭据或凭据错误时返回 401；实际应用应替换凭据检查逻辑。

绑定示例启用严格 JSON 字段模式，使用应用自己的最小校验器，分别处理格式/校验错误 400、体积超限 413、媒体类型错误 415。错误响应不直接回传底层错误。可用 `-d '{"name":""}'`、`-d '{"name":"Danny","admin":true}'` 验证拒绝路径。

优雅退出示例直接把 Engine 放进 `http.Server.Handler`；超时、信号和退出策略归应用管理。停止信号先停止接收新连接，给在途工作 10 秒完成；超时后取消请求 Context 并关闭连接，返回可识别的 shutdown 错误。请求 Context 与信号 Context 分离，避免收到 SIGTERM 就提前取消正在排空的请求。已劫持连接须由应用自行跟踪和关闭。长流、上传与下载应单独评估超时，不能机械复用示例数值。

需要明确控制 PID 时先构建：`go build -o /tmp/gola-graceful ./examples/graceful`，然后运行 `/tmp/gola-graceful` 并对该进程发送信号。应用自行管理额外资源的关闭顺序。

## 独立模块

以下示例和适配器有自己的 `go.mod` / `go.sum`，第三方依赖不会进入核心。根目录的 `go test ./...` 不会测试它们，必须进入各目录分别运行。

| 目录 | 用途与运行说明 |
| --- | --- |
| [websocket](websocket/main.go) | `cd examples/websocket && go run .`；真实握手、消息大小限制、来源检查与连接退出，见 [指南](../docs/websocket.md) |
| [contrib/otel](../contrib/otel/README.md) | OpenTelemetry 中间件；应用提供 provider，测试使用内存 exporter |

文件示例的 ID 映射仅存在于内存，实际应用需检查文件访问权限；正常退出会清理临时目录。SSE/WebSocket 示例演示连接生命周期，订阅和认证由应用补充。
