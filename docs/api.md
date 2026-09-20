# GoLa API 契约（0.1.0）

包名 `gola`，Module `github.com/starstack/gola`。具体导出声明可用 `go doc -all .` 查看。

## Engine 与路由组

```go
type HandlerFunc func(*Context)
type H map[string]any
type ErrorHandler func(*Context, error)
type Validator interface { Validate(any) error }
type RouteInfo struct { Method, Path string }
type Option func(*Engine)

func New(...Option) *Engine
func Default(...Option) *Engine
func WithLogger(*slog.Logger) Option
func WithErrorHandler(ErrorHandler) Option
func WithValidator(Validator) Option
func WithBodyLimit(int64) Option
func WithMultipartMemory(int64) Option
func WithHTMLTemplates(*template.Template) Option
func WithDisallowUnknownFields(bool) Option
func WithTrustedRealIP(bool) Option
```

`Version = "0.1.0"`，`DefaultBodyLimit = 1 << 20`，`DefaultMultipartMemory = 1 << 20`。New 无默认中间件；Default 按 RequestID、Logger、Recovery、BodyLimit 顺序安装。两个构造函数都不信任代理转发头。

Engine 嵌入 RouterGroup；两者提供：

```go
Use(...HandlerFunc)
Group(prefix string, handlers ...HandlerFunc) *RouterGroup
Handle(method, path string, handlers ...HandlerFunc)
GET(path string, handlers ...HandlerFunc)
POST(path string, handlers ...HandlerFunc)
PUT(path string, handlers ...HandlerFunc)
PATCH(path string, handlers ...HandlerFunc)
DELETE(path string, handlers ...HandlerFunc)
HEAD(path string, handlers ...HandlerFunc)
OPTIONS(path string, handlers ...HandlerFunc)
```

Engine 另有 `ServeHTTP(http.ResponseWriter, *http.Request)`、`Run(addr string) error`、`Routes() []RouteInfo`、`NoRoute(...HandlerFunc)`、`NoMethod(...HandlerFunc)`、`SetTrustedProxies([]string) error`。

- 注册路径必须以 `/` 开头。Group/Handle 的空路径表示当前分组根路径（根分组为 `/`）。分组边界只合并连接处的一个斜杠，不清理路径内部点段或重复斜杠。
- 静态优先于非空单段参数，参数优先于末尾通配符；完整匹配失败时回退。参数名不能重复。结构相同的同方法路由冲突；不同分支可各自命名参数。
- `*path` 不带前导 `/`。`/assets/` 匹配空捕获，`/assets` 不匹配 `/assets/*path`。
- 使用 `Request.URL.Path`，不再解码，不自动改大小写、尾斜杠或点路径。Query 不参与匹配。权限应使用 FullPath 或显式权限标识。
- HEAD 与 OPTIONS 显式注册；405 的 Allow 只列出显式注册方法，排序输出。未匹配请求只执行全局链，FullPath 为空。
- Routes 返回副本。所有配置只允许启动前串行修改，第一次请求冻结；后续注册/配置 panic（包括 SetTrustedProxies 的冻结检查）。代理字符串解析失败返回 error，不部分修改已有配置。
- Use 对该作用域内全部路由生效，包含 Use 之前注册的路由。空处理链、nil handler、非法选项、重复路由初始化失败。

## Context 与执行链

公开字段 `Request *http.Request`、`Writer *ResponseWriter`。

| 方法 | 行为 |
| --- | --- |
| Next / Abort / IsAborted | 正常返回自动推进；Next 执行余下链；Abort 不退出当前函数，不写响应 |
| AbortWithStatus(int) | 中断并提交状态 |
| AbortWithStatusJSON(int, any) error | 先中断再编码响应，失败返回错误 |
| Param(string) string | 返回已匹配参数，与 Request.PathValue 一致，不二次解码 |
| Query(string) string | 首个值；缺失/空都返回空串 |
| GetQuery(string) (string, bool) | 区分缺失与存在但为空 |
| DefaultQuery(key, fallback string) string | 仅缺失时使用 fallback |
| PostForm(string) string | 有界读取 Body 的 URL 编码表单；失败返回空串并仅记录一次错误 |
| GetHeader(string) string | 标准请求头读取 |
| FullPath() string | 当前完整路由模板；404/405 为空 |
| ClientIP() string | 默认 RemoteAddr；可信代理规则见安全文档 |
| Set(string, any) / Get(string) (any, bool) | 请求局部值，不映射到标准 Context |
| Error(error) / Errors() []error | 记录非 nil 错误；返回列表副本 |
| Fail(error) | 记录、Abort；未提交时调用 ErrorHandler；nil 无操作 |

`WrapH(http.Handler)` 与 `WrapF(http.HandlerFunc)` 返回 HandlerFunc，作为路由终点，返回后中断尚未执行链，已进入的中间件仍可完成后置逻辑。标准 Handler 的参数可以用 PathValue 读取。标准 Handler 的空响应/劫持连接最终生命周期交给 net/http，不额外提交响应。

WrapH/WrapF 即使安装在 NoRoute/NoMethod 中，也按标准 Handler 的隐式 200 语义执行；自定义回退 Handler 需要自己明确写出 404/405。

Context 不可跨请求保留或并发修改。请求取消用 `c.Request.Context()`；服务层接收标准 context.Context。

## 绑定与错误分类

`ShouldBindJSON(any)`、`ShouldBindXML(any)`、`ShouldBindQuery(any)`、`ShouldBindURI(any)`、`ShouldBindForm(any)` 均返回 error，不提交响应或中断。

| 入口 | 数据源 / 标签 | 限制 |
| --- | --- | --- |
| JSON | Body / json | application/json 或 application/*+json（实际 subtype），允许 charset；恰好一个 JSON 值；默认允许未知字段 |
| XML | Body / xml | *struct；application/xml、text/xml 或 application/*+xml；一个根元素，拒绝 DTD/指令，未开启外部实体或字符集转换 |
| Query | RawQuery / form | *struct；首个标量值、所有切片值；解析错误不使用部分输入 |
| URI | Param / uri | *struct；不二次解码 |
| Form | 仅 Body / form | *struct；仅 application/x-www-form-urlencoded；不混入 Query |

Query、URI 和 Form 支持 string、bool、有/无符号整数、浮点数、标量指针及标量切片。无标签使用字段名，`-` 跳过。缺失字段保留原值；空字符串仅可绑定 string，数字/布尔空值、溢出、非法转换与非有限浮点返回错误。这三个入口没有递归对象展开、map、时间类型或自定义文本转换，转换成功后才写回结构体。JSON/XML 类型语义分别沿用 encoding/json 和 encoding/xml；错误可能部分修改 dst。XML 未知元素按标准库处理，WithDisallowUnknownFields 只影响 JSON。应用校验器可自行产生副作用。

绑定成功后调用配置的 Validator；未配置不执行标签规则。Body 默认 1 MiB，对实际读取计数，未知 Content-Length 也生效。JSON 不缓存原始 Body；Form 在本请求内保留有界解析后的值供 PostForm 使用，不向 Request.Form 混入数据。

`BindingError{Kind error, Field string, Err error}` 支持 `errors.Is` 分类、`errors.As` 检查底层错误。

| sentinel | 建议响应 |
| --- | --- |
| ErrBindingSyntax / ErrBindingType | 400 |
| ErrBodyTooLarge | 413 |
| ErrUnsupportedMediaType | 415 |
| ErrValidation | 应用选择 400 或 422 |
| ErrInvalidBindingTarget | 编程错误，通常 500 |

默认 ErrorHandler 不自动按绑定错误分类，统一通用 500；调用方或自定义 ErrorHandler 负责安全映射，示例不向外输出完整底层错误。

## Multipart 上传

`MultipartForm() (*multipart.Form, error)` 解析 `multipart/form-data` 请求体；`FormFile(name string) (*multipart.FileHeader, error)` 返回该字段的第一个文件，缺失时返回 `http.ErrMissingFile`。两者不提交响应或中断处理链，解析结果与错误均按请求缓存。

`WithBodyLimit` 限制整个实际读取的请求体，默认 1 MiB，即使使用 New 也生效；限制包含 Multipart 头、字段、文件及结束边界后的尾部。`WithMultipartMemory` 设置文件部分的内存阈值，默认 1 MiB，0 表示将非空文件写入临时文件，负数配置触发 panic；这不是请求体总上限，也不是进程总内存预算。标准库另有字段与元数据预算。

表单不合并 Query，不改变 ShouldBindForm/PostForm 仅处理 URL 编码表单的契约。此前由应用自行解析的 `Request.MultipartForm` 不会被接管，返回 `ErrBindingSyntax`。媒体类型错误、格式错误和超限沿用上面的 BindingError 分类，标准库的 Multipart 资源限制也归为 `ErrBodyTooLarge`。

在处理链及后置逻辑结束后清理临时文件，Abort、解析失败与 panic 路径也执行清理。FileHeader 及其文件只在请求内使用；应用必须关闭通过 `Open()` 打开的文件，不得用客户端文件名直接构造存储路径。示例与详细行为见 [文件上传](uploads.md)。

## 响应与标准能力

```go
JSON(status int, value any) error
XML(status int, value any) error
HTML(status int, templateName string, value any) error
String(status int, format string, values ...any) error
Data(status int, contentType string, data []byte) error
Status(status int)
Header(key, value string)
Redirect(status int, location string) error
```

JSON、XML、HTML 先编码。HTML 使用配置时克隆的 html/template，模板源及函数必须可信；未配置返回 ErrHTMLTemplatesNotConfigured。完整响应助手仅接受 200–599；Status/Writer.WriteHeader 遵循标准 100–999。Status 立即提交，信息响应 100–199（除 101）不提交最终头。Header 空值删除。HEAD、204、304 无正文。完整响应助手在已提交后返回 ErrResponseCommitted；非法状态返回 ErrInvalidStatus。详细示例见 [渲染指南](rendering.md)。

Redirect 仅接受 301/302/303/307/308，以及以 `/` 开头的绝对路径或 http/https URL；非法位置/状态返回 ErrInvalidRedirect，不写响应；不读取转发前缀/主机/协议。应用仍负责目标业务白名单。

ResponseWriter 实现标准 Header/Write/WriteHeader，提供 Status() int、Size() int、Written() bool、Unwrap() http.ResponseWriter、FlushError() error。Size 为实际正文写出字节，HEAD 抑制正文不计入。Write 原始数据可连续调用，底层错误直接返回。无正文状态原始 Write 返回 http.ErrBodyNotAllowed。

使用 `http.NewResponseController(c.Writer)` 进行 Flush、Hijack、deadline 等；不保证直接类型断言 http.Flusher/Hijacker 成功。Unwrap 可达底层，不应绕过包装自行写普通响应，否则状态统计不可见。

## 文件与长连接

```go
func NewFileStore(dir string) (*FileStore, error)
func (*FileStore) Close() error
func (*FileStore) Save(context.Context, *multipart.FileHeader, string, int64) error
func (*FileStore) Static(param string) HandlerFunc
func (*Context) File(*FileStore, string) error
func (*Context) Attachment(*FileStore, string, string) error
func (*Context) SSEvent(name string, value any) error
```

FileStore 只访问预先存在且由应用管理的根目录，拒绝隐藏路径、穿越、符号链接和非普通文件。Save 按实际内容限额，临时写入后原子发布且不覆盖；应用负责授权、目录容量和生命周期。File/Attachment 仅用于 GET/HEAD，委托 net/http.ServeContent 处理范围与条件请求。Attachment 使用标准 MIME 参数编码。错误分类和平台边界见 [文件指南](files.md)。

SSEvent 将 value 编码成 JSON 后写入单个 SSE 帧并刷新。首次调用提交 200，可在同一请求中重复调用；禁止 HEAD、非法事件名、拼接普通响应，并要求底层支持刷新。应用处理写入错误、取消、期限和订阅资源释放，见 [SSE 指南](streaming.md)。WebSocket 通过 WrapH 和独立模块接入，连接升级后的退出由应用管理，见 [WebSocket 示例](websocket.md)。

## 可选包

| 包 | 入口与边界 |
| --- | --- |
| middleware/compress | New(Config) 返回标准 HTTP 中间件；显式安装的流式 gzip |
| middleware/ratelimit | New(Config) 返回 GoLa 中间件；有界的进程内令牌桶 |
| middleware/metrics | New(Config) 返回 Collector，提供 Middleware/Snapshot/ServeHTTP；有界 JSON 指标 |
| openapi | New(Routes(), Config) 返回不可变 Document；显式元数据、Bytes/ServeHTTP、OpenAPI 3.1.1 |
| contrib/otel（独立模块） | golaotel.New(Config)；应用持有 provider/exporter，默认不提取远端链路头 |

构造函数需检查 error。具体配置见 [中间件](middleware.md)、[OpenAPI](openapi.md)、[追踪适配](../contrib/otel/README.md)。核心不自动暴露指标或接口文档端点，应用负责访问控制。

## 基础中间件

`RequestID()`、`Logger()`、`Recovery()`、`BodyLimit(int64)` 都返回 HandlerFunc，可单独 Use。RequestIDKey 为 `gola.request_id`；每请求生成新128位随机ID，同步响应 X-Request-ID。默认不透传客户端ID。

Logger 用引擎 slog.Logger 记录时间、ID、方法、路由、状态、耗时、字节数、ClientIP 和异常终止标记。Recovery 不记录请求体或原始 panic 值；记录堆栈和ID。未提交时通用500；已提交时 panic(http.ErrAbortHandler) 让 net/http 中止，不追加第二份错误响应。http.ErrAbortHandler 本身保留语义。

BodyLimit 对已知超限直接413并Abort；未知长度读取返回 *http.MaxBytesError，Handler 应检查并在未提交时映射413。错误未处理时不保证自动改状态。

CORS 位于 `github.com/starstack/gola/middleware/cors`：

```go
type Config struct {
    AllowOrigins []string
    AllowMethods []string
    AllowHeaders []string
    ExposeHeaders []string
    AllowCredentials bool
    MaxAge time.Duration
}
func New(Config) (gola.HandlerFunc, error)
```

构造时校验并复制配置，来源和方法必须显式给出；来源仅完整 HTTP(S) origin 或单独 `*`，凭据与 `*` 不可组合。拒绝返回 403；有效预检返回 204，设置 Vary 并中断后续链；实际请求继续认证授权。使用 `handler, err := cors.New(config)` 后检查错误，再 `engine.Use(handler)`。更多边界见 [安全说明](security.md)。
