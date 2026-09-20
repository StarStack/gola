# 文件保存 下载和静态资源

GoLa 的 `FileStore` 将文件访问限制在指定目录内，文件响应交给标准库 `net/http.ServeContent` 处理。应用选择保存路径、配置大小上限，并负责用户身份和文件访问授权。

## 打开文件目录

```go
store, err := gola.NewFileStore("./uploads")
if err != nil {
    return err
}
defer store.Close()
```

目录必须预先存在，由应用控制。`FileStore` 可以在请求间共享，不能复制；在服务器停止接收请求并等待处理完成后关闭。关闭与保存互斥，已经打开的下载文件仍然可以读完。

路径相对于这个目录，使用 `/` 分隔。空路径、绝对路径、`.`、`..`、隐藏组件、反斜杠、冒号、控制字符、结尾空格或点和常见 Windows 设备名会被拒绝。所有层级的符号链接都被拒绝，包括指向根目录内部的链接；只有普通文件能够下载。

## 保存上传文件

```go
header, err := c.FormFile("file")
if err != nil {
    c.Fail(err)
    return
}

// 路径由服务端生成，不能直接使用 header.Filename 或用户传入的路径。
name := rand.Text() + ".upload"
if err := store.Save(c.Request.Context(), header, name, 8<<20); err != nil {
    c.Fail(err)
    return
}
```

`Save(ctx, header, name, maxBytes)` 的 `maxBytes` 必须大于零。限制按实际读取的内容执行，不依赖可修改的 `FileHeader.Size`。同时配置 `WithBodyLimit`，限制整个上传请求；文件限额和请求限额互相独立。

保存先写入权限为 `0600` 的隐藏临时文件，完成后在同一目录通过硬链接发布，不覆盖已有文件或符号链接。需要的父目录必须预先存在，文件系统必须支持硬链接。读取失败、内容超限或观察到请求取消时，会清理临时文件，不发布部分内容。清理失败会返回错误，应用应记录并处理存储故障。`Save` 不承诺断电后的持久性，也不能代替磁盘容量配额。

必须在本次请求的处理链结束前完成保存；上传临时文件会在请求结束时自动清理。文件保存后，还需由应用记录文件 ID、所有者和业务信息，下载时先检查授权，再映射到服务端存储名称。

## 下载文件

```go
// 完成业务授权，并通过数据库中的文件 ID 查到存储名称后：
if err := c.Attachment(store, storedName, "账单 2026.pdf"); err != nil {
    c.Fail(err)
}
```

`Attachment` 设置 `application/octet-stream` 和 `X-Content-Type-Options: nosniff`，使用标准库 `mime.FormatMediaType` 编码附件文件名。支持中文、空格和引号；拒绝 CR/LF、其他控制字符、路径分隔符、无效 UTF-8、空名称以及超过 255 字节的名称。附件名只是响应头元数据，不参与磁盘路径计算。

`c.File(store, name)` 直接响应文件，根据扩展名或内容决定类型。只为可信、适合在浏览器中展示的文件使用这个方法。上传 HTML、SVG 等用户内容优先作为附件下载，必要时使用独立域名和浏览器安全策略。

两个方法只接受 GET 和 HEAD；HEAD 需要显式注册路由。打开失败时不写响应，已提交响应返回 `ErrResponseCommitted`。响应开始后，Range 等协议错误由 `net/http.ServeContent` 直接输出，该标准库函数不返回传输错误。

`ServeContent` 处理 Range、HEAD、Last-Modified 和条件请求。若应用设置有效 ETag，也会使用它进行条件判断；GoLa 不自动生成 ETag。文件应保持不可变，修改内容时发布一个新的存储名称，避免正在下载的文件被外部进程截断。

## 静态资源

```go
assets, err := gola.NewFileStore("./public")
if err != nil {
    return err
}
defer assets.Close()

r.GET("/assets/*path", assets.Static("path"))
r.HEAD("/assets/*path", assets.Static("path"))
```

`Static` 从指定的通配参数读取相对路径，并终止后续处理链。它不提供目录列表、默认首页或自动跳转。目录、隐藏文件、非法路径和符号链接返回 404，错误响应不包含主机路径。只挂载专门的公开资源目录，不挂载项目根目录、用户主目录或上传私有资料的目录。

## 错误处理

| 错误 | 含义 |
| --- | --- |
| `ErrInvalidFilePath` | 存储路径不符合限制 |
| `ErrInvalidFileLimit` | 单文件限额不是正数 |
| `ErrFileTooLarge` | 实际文件内容超过限额 |
| `ErrInvalidUpload` | 空上传参数或上传源不可读取 |
| `ErrInvalidDownloadName` | 附件名称不能安全使用 |
| `ErrFileMethod` | 请求方法不是 GET 或 HEAD |
| `fs.ErrExist` | 保存目标已存在 |
| `fs.ErrNotExist` | 文件或目录不存在，或不允许访问该类型 |
| `fs.ErrPermission` | 操作系统拒绝文件操作 |
| `ErrFileStoreClosed` | 文件目录已经关闭或未初始化 |
| `ErrFileStoreIO` | 其他文件操作失败，主机路径已隐藏 |
| `ErrFileStorePlatform` | 当前平台不具备支持的文件访问能力 |
| `context.Canceled` / `context.DeadlineExceeded` | 保存时观察到请求取消或超时 |

## 安全边界和平台

文件访问基于标准库 `os.Root`。下载在打开文件前后检查文件类型和身份；Unix 使用非阻塞打开，避免文件在检查后被替换为 FIFO 导致请求挂起。支持 Linux、macOS、常见 BSD、AIX、Solaris 和 Windows；其余平台创建 `FileStore` 会返回 `ErrFileStorePlatform`。不同平台和文件系统的实际部署行为仍需在目标环境验证。

`os.Root` 不隔离硬链接、挂载点或设备；GoLa 会拒绝非普通文件，但不会识别指向敏感普通文件的硬链接或跨挂载的普通文件。存储目录必须由可信应用管理，不能允许不可信本机进程重命名其目录、创建硬链接或挂载。文件系统约束不能代替文件访问授权、内容扫描、访问配额和备份。

有关标准库的准确语义，参见 [os.Root](https://pkg.go.dev/os#Root)、[net/http.ServeContent](https://pkg.go.dev/net/http#ServeContent) 和 [mime.FormatMediaType](https://pkg.go.dev/mime#FormatMediaType)。

## 运行示例

在项目根目录运行：

```bash
go run ./examples/files
```

另开终端上传文件：

```bash
curl -F 'file=@README.md' http://127.0.0.1:8080/upload
```

响应返回 `id` 和 `download` 路径。将返回的 `id` 填入以下命令：

```bash
curl -o downloaded.bin http://127.0.0.1:8080/files/返回的id
```

示例只监听 `127.0.0.1`，使用独立临时目录和内存中的 ID 映射；重启后映射失效。在服务终端按 Ctrl+C（或发送 SIGTERM）后，服务停止接收新请求，并给已经开始的请求 10 秒完成；超过期限则取消请求并关闭连接。等待处理函数结束后，先关闭 `FileStore`，再删除本次创建的临时目录。监听启动失败时同样执行清理；SIGKILL、进程崩溃或断电仍可能留下临时文件。它演示保存和下载协议，不是带登录、持久化元数据或访问授权的文件服务。
