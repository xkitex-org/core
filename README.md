# core

独立 Go module：`go.chaoxizhongzhi.com/xkitex/core`，对外 Go 包名为 `x`，Kitex v0.16.4。业务直接导入，无需别名。

```go
import "go.chaoxizhongzhi.com/xkitex/core"

err := x.Server(
    x.WithService(productservice.RegisterService, productHandler),
    x.WithService(inventoryservice.RegisterService, inventoryHandler),
)
```

## 启动配置

启动前必须提供以下环境变量：

```sh
export X_SERVICE_GROUP=zmall
export X_SERVICE_NAME=product
export X_CONSUL_ADDR=127.0.0.1:8500
export X_CONSUL_TOKEN='<具有入口和全部引用 Key 读取权限的 ACL Token>'
```

GROUP 和 NAME 去除首尾空白后不能为空，以字母或数字开头，仅允许字母、数字、点、下划线和连字符，拒绝路径分隔符或目录跳转。应用服务名固定为 `GROUP-NAME`；示例为 `zmall-product`，不从 YAML 的 service.name 读取，也不改变 IDL Service 名。

使用官方 `github.com/hashicorp/consul/api` SDK，固定从 Consul KV `config/GROUP/NAME/application.yaml` 读取全局配置，示例为 `config/zmall/product/application.yaml`。不使用 X_CONFIG_KEY，不回退到本地文件或其他 Key。X_CONSUL_ADDR 支持 host:port（默认 HTTP）或完整 HTTP(S) 地址；不能包含账号密码、路径、查询参数或片段。Token 仅作为请求头发送。

KV Value 是一个 YAML 对象，例如：

```yaml
imports:
  - config/zmall/shared/database.yaml
  - config/zmall/shared/redis.yaml

service:
  port: 8888

# 覆盖共享配置中的 host，其余数据库字段继续使用共享值。
database:
  postgres:
    host: application-database.internal
```

例如 `config/zmall/shared/database.yaml` 的内容为：

```yaml
database:
  postgres:
    host: shared-database.internal
    port: 5432
    username: application
```

最终数据库配置的 host 为 `application-database.internal`，port 和 username 保留共享值。当前 core 只消费合并后的 service.port，其他配置节参与加载和合并，不增加业务配置的公共读取 API。

- imports 可省略或设置为空列表；存在时必须是字符串列表，仅接受 `config/` 下的明确 Key，不支持本地文件、URL、相对路径和通配符。
- 入口及每个引用 Key 在启动期间各读取一次，同一启动过程共用 SDK 客户端和 ACL Token；不扫描目录、不建立监听。
- 先按 imports 声明顺序合并，后面的引用覆盖前面的；最后合并入口内容，`application.yaml` 优先级最高。
- 对象递归合并字段；列表、标量、null 及不同类型整体替换，不按列表下标合并。高优先级的空对象不会清除低优先级对象的已有字段。
- 重复引用、自引用均报错；本次仅支持入口声明 imports，引用文件不能再次声明 imports。
- 合并后的 service.port 缺省时使用 8888；省略 service 节也使用默认端口。
- 显式端口必须为 1..65535 的 YAML 整数。0、null、字符串、浮点数和越界值均报错，不回退默认值。
- 先完成全部配置合并，再校验最终端口；入口的非法端口不会回退到引用端口。单个文件中的重复字段、非法 YAML、多文档或非字符串字段名仍报错。
- 必需环境变量缺失、任一配置不可达/无读取权限、Key 不存在或内容非法均阻止启动，即使其字段会被入口覆盖也不跳过读取。
- 配置在任何 Server 创建、Service 注册和监听之前完成加载；后续配置修改需要重启服务才生效。每次 HTTP 读取超时为 10 秒，每个文件接受的 YAML 配置上限为 1 MiB。
- 返回的错误不附带配置正文或服务端错误响应正文。

全部配置加载及最终端口校验通过后，使用 Kitex `klog.Infof` 输出覆盖日志。每个被覆盖字段只记录一次，按字段名排序，显示最终生效配置文件的完整 Consul Key，不输出中间覆盖过程、原来源或配置值；仅新增的字段不输出。对象按最终字段记录，列表整体替换时记录列表字段名。

```text
配置[database.postgres.host]已被[config/zmall/product/application.yaml]覆盖。
```

core 不修改 klog 的输出、级别或 Logger，后续由独立日志配置包统一设置。

Consul ACL 可让研发只修改入口、由管理员维护共享文件；应用 Token 必须能读取所有引用。ACL 保护共享文件本身，入口仍可覆盖共享字段，覆盖行为遵循上述优先级。

读取并校验成功后，core 把应用服务名和监听端口交给 Kitex，再执行业务注册和启动。当前不增加平台校验、配置热更新或 Consul 实例注册。

后续服务注册与发现使用 `github.com/kitex-contrib/registry-consul`；服务治理后续接入 Sentinel，当前不引入 `config-consul`。官方 Consul SDK 固定为 v1.34.1，Go 最低版本为 1.25.9。

## 注册与包边界

Handler 由应用创建。WithService 绑定生成的 RegisterService 函数和 Handler，类型、nil 和选项有效性在执行任何注册前校验。所有 Service 按顺序注册到同一个 Server，全部成功后才监听。没有 Service、空回调、空/错误 Handler、重复 Service 或注册失败均会阻止启动。

Server 阻塞运行，返回启动、运行和收尾错误；退出信号和优雅停机由 Kitex 处理。多个 Service 按 IDL Service 名分流，同名方法可以共存。Thrift 客户端需使用 TTHeader 和 transmeta.ClientTTHeaderHandler 传递服务名。

```text
core/
  go.mod
  go.sum
  server.go           # Server、ServerOption
  service.go          # WithService
  internal/config/    # 环境校验、固定入口及引用读取、配置合并和端口解析
  internal/startup/   # 注册、底层 Kitex 创建与运行
```

根包仍只公开 Server、WithService、ServerOption。ServerOption 没有公开字段或方法，零值无效。公开 Handler 参数为 any，兼容注册函数接收 IDL 接口、应用传入具体指针的形式；错误类型会在启动前返回。internal 实现不能由外部业务项目直接导入。

从 xkitex 目录进入独立模块验证：

```sh
cd core
GOWORK=off GOTOOLCHAIN=local go test -race -timeout 45s ./...
GOWORK=off GOTOOLCHAIN=local go build ./...
```

测试使用模拟 Consul HTTP 服务和本机临时端口，不依赖真实 Consul 数据。xkitex 上层目录没有 go.mod、go.work 或 replace。
