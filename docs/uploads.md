# 文件上传

GoLa 的 `MultipartForm` 与 `FormFile` 使用 Go 标准库解析 `multipart/form-data`。这两个入口只读取请求体，不合并 Query，不自动提交响应；成功结果与解析错误都会在当前请求内缓存。

## 运行示例

在项目根目录启动：

```sh
go run ./examples/upload
```

另开终端，在项目根目录上传一份文件：

```sh
curl -F 'file=@README.md' -F 'description=GoLa example' http://127.0.0.1:8080/upload
```

示例返回文件大小、SHA-256 摘要和 `description` 字段，不持久保存文件，也不把客户端文件名作为服务器路径。实现见 [upload/main.go](../examples/upload/main.go)。

## API 与配置

```go
r := gola.New(
    gola.WithBodyLimit(8 << 20),
    gola.WithMultipartMemory(64 << 10),
)
```

| 配置或方法 | 行为 |
| --- | --- |
| `WithBodyLimit` | 整个实际读取请求体的上限，默认 1 MiB；包括 MIME 头、字段、文件和结束边界后的尾部 |
| `WithMultipartMemory` | 文件部分的内存阈值，默认 1 MiB；超过阈值使用标准库临时文件，0 可让非空文件使用临时文件，负值不合法 |
| `c.MultipartForm()` | 返回标准 `*multipart.Form`，通过 `Value` 和 `File` 按字段读取所有值和文件 |
| `c.FormFile("file")` | 返回该字段的第一个 `*multipart.FileHeader`；文件不存在返回 `http.ErrMissingFile` |
| `header.Open()` | 打开文件供读取；调用方必须关闭返回的文件 |

内存阈值不是上传总大小限制。标准库还为普通字段和元数据保留额外预算；应用仍应设置请求总大小、服务超时及并发限制。多个请求的资源消耗会累加。

以上限制在 `New()` 上也生效，无需依赖默认中间件。`ShouldBindForm` 和 `PostForm` 仍仅处理 URL 编码表单；读取 Multipart 普通字段请使用 `form.Value`，不要混用标准 `Request.FormValue`/`PostFormValue`，GoLa 不填充它们使用的字段缓存。不要在调用 GoLa 的上传 API 前先用标准方法解析同一个请求；已经设置 `Request.MultipartForm` 时会返回错误，避免复用未经 GoLa 限制的输入。

## 处理错误

使用 `errors.Is` 判断错误，再由应用决定 HTTP 响应：

| 错误 | 常用状态码 |
| --- | --- |
| `gola.ErrUnsupportedMediaType` | 415 |
| `gola.ErrBodyTooLarge` | 413 |
| `gola.ErrBindingSyntax`、`http.ErrMissingFile` | 400 |

`ErrBodyTooLarge` 也包括标准库对 Multipart 部件数量或元数据的限制。底层错误可通过 `errors.As` 查看，不应直接回传给客户端。解析失败不会返回可继续使用的部分表单。

## 文件生命周期

临时文件在当前处理链和中间件后置逻辑完成后清理；正常返回、Abort、解析失败、panic 和连接中止都包含清理路径。临时文件不是持久存储。

需要保存上传内容时，应在请求结束前完成复制，使用服务端生成的文件名和受控目录，并处理复制失败。文件名、扩展名和上传的 Content-Type 都是客户端输入，不能证明内容安全，也不能用于绕过鉴权。框架不会根据它们创建目标目录或覆盖已有文件。

不要把 Context、FileHeader 或临时文件交给请求结束后的后台任务。后台处理应使用已经持久保存的数据以及独立的标准 Context。

标准库语义见 [multipart.ReadForm](https://pkg.go.dev/mime/multipart#Reader.ReadForm) 与 [Form.RemoveAll](https://pkg.go.dev/mime/multipart#Form.RemoveAll)。
