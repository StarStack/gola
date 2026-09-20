# 安全使用指南

GoLa 的 HTTP 协议、TLS 和连接处理来自 Go 标准库 `net/http`。使用受支持 Go 系列的最新安全补丁，并在升级工具链或依赖后重新验证应用。

## 默认行为

| 边界 | GoLa 的默认行为 |
| --- | --- |
| 代理头 | 不信任任何代理，ClientIP 只使用直接连接地址 |
| 请求绑定 | JSON/XML/Form/Multipart 默认限制实际读取量为 1 MiB，New 也生效 |
| Request ID | 生成新的随机 ID，不透传客户端提供的 ID |
| 日志 | 记录路由模板；未匹配路径截断并转义；不记录完整 Query、Body、Authorization 或 Cookie |
| Panic | 未提交响应时返回通用 500；已提交时中止响应，不追加错误正文 |
| CORS | 默认关闭，应用显式配置允许的来源、方法与请求头 |
| 路径 | 使用标准库解析后的 URL.Path，不二次解码、不自动重定向或清理路径 |

`Default()` 安装 RequestID、Logger、Recovery、BodyLimit。`New()` 不安装这些中间件，但绑定 API 自身仍限制请求体大小。

## 配置可信代理

仅把实际受控代理的 IP 或 CIDR 放入列表，并在接收请求前配置：

```go
r := gola.Default()
if err := r.SetTrustedProxies([]string{"10.20.0.10/32"}); err != nil {
    log.Fatal(err)
}
```

以上地址为配置示例，需要替换为你的代理地址。未部署代理时保留默认配置即可，不要使用 `0.0.0.0/0` 或 `::/0` 信任所有来源。

只有直接连接来自可信代理时，ClientIP 才读取 X-Forwarded-For。先验证整条链，再从右向左选择第一个不可信地址；非法或空的已提供 XFF 会回退到直接连接地址，不改用其他头。链最多 8 KiB、128 个地址，拒绝带端口或 zone 的转发地址。

`WithTrustedRealIP(true)` 仅在直接连接可信且未提供 XFF 时启用 X-Real-IP。边缘代理应正确清除或追加客户端转发头。ClientIP 可用于日志与限流输入，不能替代身份认证。

## 限制请求体并处理绑定错误

```go
r := gola.Default(
    gola.WithBodyLimit(1 << 20),
    gola.WithDisallowUnknownFields(true),
)
```

限制覆盖未知长度和分块输入的实际读取，不仅检查 Content-Length。ShouldBind 系列只返回错误，由 Handler 选择响应；可用 `errors.Is(err, gola.ErrBodyTooLarge)` 返回 413，媒体类型错误返回 415。完整例子见 [绑定示例](../examples/binding/main.go)。

手动读取 Body 时需要安装 BodyLimit，并处理 `*http.MaxBytesError`。已提交的响应不能再改成 413。

MultipartForm/FormFile 同样执行实际读取限制，包括结束边界后的尾部；WithMultipartMemory 只控制文件的内存阈值，不能替代 WithBodyLimit。框架在请求结束和错误路径清理自己创建的临时文件，应用负责关闭打开的文件，并在请求内完成必要的持久化。不要使用客户端提供的文件名作为服务器存储路径；媒体类型和扩展名也不能替代内容验证。完整示例见 [文件上传](uploads.md)。

XML 绑定拒绝 DTD/指令、多个根和尾部文本，不启用外部实体。HTML 使用 html/template 的自动转义；模板源、函数以及 template.HTML 等绕过转义的类型必须由应用信任，不能直接接受用户提供的模板。

## 文件、流式响应与可选扩展

FileStore 使用 os.Root，拒绝路径穿越、隐藏路径和符号链接，保存时不覆盖已有文件。专用存储目录仍须由可信进程控制；硬链接和挂载不是 os.Root 的隔离边界。下载附件名经过验证并由 mime.FormatMediaType 编码。私有文件仍须先检查业务授权；不要将上传的 HTML/SVG 直接作为同源网页展示。详见 [文件指南](files.md)。

SSE 应逐次设置写入期限并观察取消；WebSocket 升级连接不由 Server.Shutdown 自动关闭，示例显式登记、关闭和等待连接退出。连接时检查 Origin 不等于业务认证，应用仍应限制身份、订阅权限和总连接数量。

gzip 为显式启用，不用于同时包含秘密和攻击者可控输入的响应。限流状态有上限，但只在当前进程共享；多实例全局配额由应用或网关提供。指标与 OpenAPI 不自动对外暴露；管理员选择端点和访问控制。追踪默认不信任远端 traceparent，不采集原始请求头、URL、正文或 panic 值。

## 中间件与权限边界

处理器正常返回会自动推进后续链。鉴权失败应调用 AbortWithStatus 或 AbortWithStatusJSON 后立即 `return`；只是不调用 Next 无法阻止业务执行。

路径权限判断应使用 `FullPath()` 或显式权限标识，避免独立实现第二套解码和路径规范化。`Param` 与标准 `Request.PathValue` 使用同一匹配结果。用户身份和资源访问权限由应用校验，路由匹配本身不提供授权。

Context 仅在当前请求内有效。后台任务应复制必要业务数据，创建独立的标准 Context 和截止时间，不得继续使用请求的 ResponseWriter。

## 跨域与重定向

`middleware/cors` 只接受配置的完整 HTTP(S) 来源，或不携带凭据时单独使用 `*`。不支持通配域名或 `null` 来源。有效预检返回 204，实际请求仍执行后续认证授权；CORS 不提供 CSRF 防护。

Redirect 仅接受应用传入的绝对路径或 HTTP(S) URL，拒绝 CR/LF、协议相对地址、反斜杠、危险协议和用户信息部分；不使用转发头构造地址。如果跳转目标来自用户输入，应用仍需要设置允许的目标域名。

## 日志与异常

日志使用 `slog.Logger`，应用负责输出目的地、访问权限与保留时间。Recovery 记录请求 ID、panic 类型和堆栈，不输出原始 panic 值或请求快照。不要在自定义日志或 ErrorHandler 中回传密钥、完整底层错误或业务敏感数据。

响应已提交后发生 panic，Recovery 通过 `http.ErrAbortHandler` 让标准服务器中止传输。接收方应把截断响应视为失败，应用也应停止重试向已断开的连接写入。

## 历史安全问题的设计参考

以下问题用于解释默认行为与回归覆盖，不表示 GoLa 依赖受影响的 Gin 版本。参考实现基线为 [Gin v1.12.0](https://github.com/gin-gonic/gin/tree/v1.12.0)。

| 上游记录 | GoLa 的对应防护 |
| --- | --- |
| [GO-2020-0001 / CVE-2020-36567](https://pkg.go.dev/vuln/GO-2020-0001)：Gin 路径日志注入 | 结构化日志与未匹配路径转义；middleware_test.go 包含日志注入回归 |
| [GO-2021-0052 / CVE-2020-28483](https://pkg.go.dev/vuln/GO-2021-0052)：代理头伪造客户端 IP | 默认不信任代理、完整链校验；proxy_test.go 包含信任边界测试与 fuzz |
| [GO-2023-1737 / CVE-2023-29401](https://pkg.go.dev/vuln/GO-2023-1737)：附件文件名影响 Content-Disposition | Attachment 验证名称并用 mime.FormatMediaType 编码，包含头注入与 fuzz 回归；应用仍负责文件授权 |

X-Forwarded-Prefix 相关的 [CVE-2023-26125](https://nvd.nist.gov/vuln/detail/cve-2023-26125) 存在分类分歧：Go 官方 [vulndb #1755](https://github.com/golang/vulndb/issues/1755) 将其排除为 `NOT_A_VULNERABILITY`。GoLa 仍不自动按转发前缀生成重定向地址。

HTTP/2 的 [GO-2023-1571](https://pkg.go.dev/vuln/GO-2023-1571) 等问题属于标准库或 x/net，安全更新依赖工具链和依赖升级；仅依赖标准库不意味着无需漏洞检查。

## 漏洞检查

根模块与所有独立模块需要分别检查；测试中的依赖也纳入扫描：

```sh
govulncheck -test ./...
for module in benchmarks contrib/otel examples/websocket; do
  (cd "$module" && govulncheck -test ./...) || exit 1
done
```

CI 使用固定版本扫描器，见 [工作流](../.github/workflows/ci.yml)。扫描反映所用数据库与调用路径中的已知问题，仍需结合回归测试和代码审查。项目尚未公开发布，正式安全报告渠道会在发布时公布。

独立基准使用外部测试包，CI 通过 `-test -show verbose` 同时展示依赖图。检查结果时确认实际包含对照框架与第三方依赖，不能只看空常规包的成功退出。扫描区分可达符号、导入包和整个模块：没有导入的遗留包也可能带有模块级记录，其处置与适用范围见 [基准说明](benchmarks.md)。
