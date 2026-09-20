# HTML 与 XML

GoLa 使用标准库 `html/template` 和 `encoding/xml`。HTML 与 XML 响应先在内存中完整编码，成功后才提交状态和响应头；编码失败时可以调用 `c.Fail(err)` 返回统一错误。大型输出的内存预算由应用控制。

## HTML 模板

启动前解析模板并传入 `WithHTMLTemplates`：

```go
templates := template.Must(template.New("hello").Parse(`<h1>Hello {{.Name}}</h1>`))
r := gola.Default(gola.WithHTMLTemplates(templates))
r.GET("/hello", func(c *gola.Context) {
    if err := c.HTML(http.StatusOK, "hello", gola.H{"Name": c.Query("name")}); err != nil {
        c.Fail(err)
    }
})
```

`WithHTMLTemplates` 克隆模板集合，原集合后续重新解析不会改变引擎模板。配置遵循引擎的启动前规则；nil 模板、已经执行而无法克隆的模板或服务开始后重新配置都会 panic。未配置模板时 `HTML` 返回 `ErrHTMLTemplatesNotConfigured`；名称不存在或执行失败时返回模板错误。应用模板函数和其捕获的共享状态需要支持并发调用。

模板源码和模板函数必须由应用维护；请求数据只能作为执行参数，不能解析成模板。`html/template` 根据 HTML 上下文转义普通字符串；将不可信数据转换为 `template.HTML`、`template.JS`、`template.URL` 等可信类型会绕过相应保护。详见 [Go HTML 模板安全模型](https://pkg.go.dev/html/template#hdr-Security_Model)。

## XML 响应与绑定

`c.XML(status, value)` 使用 `encoding/xml.Marshal`，返回 `application/xml; charset=utf-8`，不自动添加 XML 声明。字段、根元素与命名空间使用标准 `xml` 标签。

`c.ShouldBindXML(&input)` 的目标必须是非 nil 结构体指针，接受 `application/xml`、`text/xml` 和具体的 `application/*+xml` 媒体类型（如 `application/vnd.example+xml`）。它只返回错误，不写响应或中断中间件：

- 全部请求体受 `WithBodyLimit` 限制，`New` 和 `Default` 都生效。
- 必须恰好一个根元素；拒绝根元素之外的非空白文本、额外根元素、DOCTYPE、DTD 和一般处理指令。允许根元素前的 XML 声明及注释。
- 不加载外部实体，不安装字符集转换器；解析与字段映射采用标准库规则。
- 成功绑定后调用已配置的 `Validator`；转换失败可能部分修改目标。
- 未知元素采用 `encoding/xml` 的默认行为，`WithDisallowUnknownFields` 仅影响 JSON。此接口不提供 XML Schema 验证。

错误可使用 `errors.Is` 判断：`ErrInvalidBindingTarget`、`ErrUnsupportedMediaType`、`ErrBodyTooLarge`、`ErrBindingSyntax`、`ErrBindingType`、`ErrValidation`。数值转换错误归为 `ErrBindingType`；`BindingError` 保留底层错误。标准映射规则见 [encoding/xml](https://pkg.go.dev/encoding/xml#Unmarshal)。

## 运行示例

在项目根目录运行：

```bash
go run ./examples/render
```

另开终端：

```bash
curl 'http://127.0.0.1:8080/hello?name=Danny'
curl -H 'Content-Type: application/xml' --data '<greeting><name>Danny</name></greeting>' http://127.0.0.1:8080/xml
```

完整代码见 [examples/render](../examples/render/main.go)。HTML 与 XML 同样遵循普通响应的已提交检查、HEAD、204 和 304 行为。
