# OpenAPI 文档

`github.com/starstack/gola/openapi` 将已注册路由和应用显式提供的接口描述生成 OpenAPI 3.1.1 JSON。它仅依赖标准库，不读取请求流量，也不推断 Go 结构体、校验规则或业务权限。

运行 `go run ./examples/openapi`，访问 `/hello/Danny` 与 `/openapi.json`。生成的 JSON 可交给支持 OpenAPI 3.1 的文档界面或客户端生成工具；GoLa 不内置外部 CDN 脚本或 Swagger UI 页面。

在业务路由注册后，将 `r.Routes()` 交给 `openapi.New`，再用 `gola.WrapH(document)` 注册文档的 GET/HEAD 入口。文档在创建时固定，后续注册的路由不会自动出现。建议最后注册文档入口，避免将文档本身误计入业务 API。

配置中的 `Title` 与 `Version` 必填。`Operations` 的键是原始方法和路由，例如 `GET /users/:id`；可显式描述 operationId、摘要、标签、参数、请求体和响应。Schema 使用应用定义的 JSON Schema 对象，不进行全量 JSON Schema 语义验证。配置应来自受信任的程序代码，应用须保持文档与实际处理器一致。

`/:id` 转换为 `/{id}`，缺省补充必填字符串路径参数。末尾 `*path` 转为 `/{path}` 并增加 `x-gola-catch-all` 标记，说明可跨段及空值行为；普通 OpenAPI 工具不一定支持这项扩展，应按客户端能力明确设计下载接口。相同形状但参数名称不同的路由、无法表示的字面花括号路径、不支持的方法以及引用不存在路由的配置，会在创建时返回错误。

没有显式响应描述时仅生成 `default: Application-defined response`，不会猜测状态码或数据结构。框架会检查基本路径/参数/响应结构和重复 operationId；复杂 schema 和业务权限由应用验证。

`Document` 实现 `http.Handler`，支持 GET、HEAD 和 ETag 条件请求。响应是创建时的不可变快照，修改原配置或 `Bytes()` 返回值不会修改线上文档。生产环境应根据接口信息的可见性决定是否为文档入口增加认证。

参考：[OpenAPI 3.1.1 规范](https://spec.openapis.org/oas/v3.1.1.html)。
