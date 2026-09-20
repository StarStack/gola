# 性能基线

GoLa v0.1.0 提供可复现的 HTTP 处理微基准，不声称比其他框架更快。跨框架代码位于独立 `benchmarks/` 模块，固定 [Gin v1.12.0](https://github.com/gin-gonic/gin/releases/tag/v1.12.0)，`go.mod` 和 `go.sum` 保存全部依赖版本。根模块仍只依赖标准库。

## 测量方法

三方均直接调用标准 `http.Handler.ServeHTTP`，使用相同 GET 请求、响应状态、响应正文和响应头。`TestEquivalentResponses` 逐项比较 status、全部 Header 和 Body；只有等价性检查通过后才记录基准。GoLa 和 Gin 使用 New，关闭日志、Recovery、Request ID 和请求体限制中间件。Gin 禁止路径重定向并关闭可信代理，启用方法不匹配检测。

| 场景 | 输入与有效工作 |
| --- | --- |
| Static | `/hello`，文本 `hello` |
| Param | `/users/42`，读取匹配参数并输出 `42` |
| NotFound | `/missing`，自定义 404 与相同 `not found` 文本 |
| JSON | `/json`，实际编码结构体得到 `{"message":"hello"}` |
| Middleware5 | 五个中间件各写一个响应头，完整执行下一层，再输出 `hello` |
| Routes10 / 100 / 1000 | 恰好 10 / 100 / 1000 条业务静态路由，请求最后注册路径；标准 ServeMux 另有仅处理 404 的 fallback |

路由注册、请求构造和首次请求初始化不计时。每次操作创建新的 `httptest.ResponseRecorder`，因此数据包含共同的响应记录器分配成本，并非纯树查找耗时。请求对象在串行循环中复用，无网络、TLS、连接、数据库或外部服务成本。所有框架都只执行相同的请求功能，但并不代表它们所有 API 或默认行为相同。

根 `benchmark_test.go` 还提供只依赖标准库的 GoLa 基准。根基准的 `Middleware` 只执行五个调用 `Next` 的中间件，`NotFound` 使用默认 404，因此与跨框架场景分别保存、分别比较。可选中间件及文件、模板、XML、SSE、WebSocket 的业务成本不由这些用例衡量。

## 复现

以下命令从仓库根目录开始，先验证，再顺序测量。两组测量期间应暂停其他项目编译和测试。

```bash
export GOTOOLCHAIN=go1.27.1

# 验证根模块及独立基准模块
go test ./...
(cd benchmarks && go mod verify && go test ./... && go test -race ./... && go vet ./...)

# 根模块
go test -run='^$' -bench='^BenchmarkGoLa$' -benchmem -benchtime=100ms -count=3 -cpu=1 .

# 独立模块，根目录 go test ./... 不会遍历嵌套模块
cd benchmarks
go test -run='^$' -bench='^BenchmarkComparison$' -benchmem -benchtime=100ms -count=3 -cpu=1 .
```

首次下载工具链和依赖需要访问 Go module proxy。真实网络测试需要允许监听本机临时端口。

## 实测环境和结果

采样日期为 2026-09-20，环境为 Apple M5 Max、Darwin 25.6.0 / arm64、Go 1.27.1。采样使用 `-cpu=1`（GOMAXPROCS=1），每个用例 100ms、3 次独立样本；没有使用 `RunParallel`。等价性检查通过后，根基准与对比基准顺序运行，采样期间没有并行执行项目测试或编译。测量未隔离整台机器，短样本只适合作为初始基线。

原始结果：

- [GoLa v0.1.0 根模块基准](../benchmarks/results/gola-v0.1.0-go1.27.1-darwin-arm64.txt)
- [GoLa v0.1.0 / Gin / net/http 对比](../benchmarks/results/comparison-v0.1.0-go1.27.1-darwin-arm64.txt)

以下 ns/op 取 3 次中位数；B/op 和 allocs/op 在 3 次采样中一致。它们是直接 ServeHTTP 微基准，不能推广为生产服务性能排名。未运行端到端 HTTP 压测，因此不提供吞吐率或延迟分位数，也不把 ns/op 换算成生产 QPS。

### 根模块

| 场景/实现 | ns/op 中位数 | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Static | 392.3 | 1248 | 14 |
| Param | 517.0 | 1632 | 18 |
| NotFound | 416.6 | 1280 | 14 |
| JSON | 478.9 | 1272 | 14 |
| Middleware | 405.3 | 1248 | 14 |

### 跨框架比较

| 场景/实现 | ns/op 中位数 | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Static/GoLa | 423.4 | 1248 | 14 |
| Static/Gin | 386.2 | 1040 | 9 |
| Static/NetHTTP | 345.2 | 1013 | 10 |
| Param/GoLa | 548.3 | 1632 | 18 |
| Param/Gin | 438.6 | 1072 | 11 |
| Param/NetHTTP | 380.6 | 1032 | 11 |
| NotFound/GoLa | 456.9 | 1264 | 14 |
| NotFound/Gin | 387.1 | 1040 | 9 |
| NotFound/NetHTTP | 465.8 | 1104 | 15 |
| JSON/GoLa | 500.0 | 1272 | 14 |
| JSON/Gin | 495.9 | 1048 | 11 |
| JSON/NetHTTP | 458.1 | 1048 | 11 |
| Middleware5/GoLa | 680.9 | 1408 | 19 |
| Middleware5/Gin | 746.5 | 1200 | 14 |
| Middleware5/NetHTTP | 592.6 | 1173 | 15 |
| Routes10/GoLa | 433.5 | 1264 | 14 |
| Routes10/Gin | 380.7 | 1040 | 9 |
| Routes10/NetHTTP | 373.4 | 1013 | 10 |
| Routes100/GoLa | 439.0 | 1264 | 14 |
| Routes100/Gin | 382.4 | 1040 | 9 |
| Routes100/NetHTTP | 371.3 | 1013 | 10 |
| Routes1000/GoLa | 473.0 | 1264 | 14 |
| Routes1000/Gin | 401.9 | 1040 | 9 |
| Routes1000/NetHTTP | 436.8 | 1013 | 10 |

本次用例中 GoLa 的请求分配数量高于 Gin。当前实现保留独立的请求状态和请求结束时的 Multipart 清理，未引入 Context 池。后续优化应在相同用例和安全回归全部通过的前提下重新采样，不能把此次短测当作稳定的跨版本性能承诺。生产吞吐、尾延迟和并发连接容量仍需在实际部署环境测试。

## 基准依赖的安全检查

基准使用外部 `benchmarks_test` 测试包，普通包由 `benchmarks/doc.go` 声明。这样测试入口及其真实第三方依赖具有独立包路径，避免 govulncheck v1.8.0 将同路径的普通包和测试增强包去重后漏掉测试依赖。升级扫描工具后，也应核对实际覆盖范围，不能只看退出码；当前构建图应包含 Gin、GoLa 和传递依赖。该行为可对照工具的 [PackageGraph 实现](https://github.com/golang/vuln/blob/v1.8.0/internal/vulncheck/packages.go)。

```bash
cd benchmarks
go list -test -deps ./...
govulncheck -test -show verbose ./...
```

当前基准模块使用 `quic-go` v0.59.1、`x/crypto` v0.56.0、`x/net` v0.57.0、`x/sys` v0.47.0、`x/text` v0.41.0。这些版本包含 [HTTP/3 QPACK 问题](https://pkg.go.dev/vuln/GO-2026-5676)等已知问题的修复。实际版本以本模块的 `go.mod` / `go.sum` 为准，不进入 GoLa 核心依赖；上述性能结果使用这些依赖采样。

2026-09-20 在 Go 1.27.1 上的详细扫描列出 22 个模块，没有已调用符号或已导入包的漏洞结果。仍保留一项仅模块级的 [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932)：`x/crypto/openpgp` 已停止维护且没有修复版本；当前构建图不导入该包。这个结果不表示模块内所有未使用的包都没有问题，新增依赖或构建标签后必须重新扫描。
