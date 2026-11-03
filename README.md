# core：服务启动与配置

独立 Go 模块：`go.chaoxizhongzhi.com/xkitex/core`，对外 Go 包名为 `x`，Kitex v0.16.4。业务直接导入，无需别名。

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

最终数据库配置的 host 为 `application-database.internal`，port 和 username 保留共享值。core 直接使用合并后的 `service.port`；显式传入且支持配置的日志实现接收 `logging` 节。其余配置节参与加载和合并，当前不增加业务配置的公共读取接口。

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

未传 `WithLogger` 时，core 不修改现有 klog 日志实现、输出或级别，也不调用其配置、关闭或刷新方法。

启动期间同样使用 Kitex `klog` 输出以下信息：校验后的实际 Consul 协议和地址、派生服务名、固定入口 Key，每个入口或引用文件开始读取及 YAML 解析成功、文件合并数量、最终端口及其生效文件（缺省时标记默认值），随后记录准备监听的地址及已注册的 IDL Service 名称。配置加载失败使用 Error 日志记录安全错误，并终止启动；失败时不输出服务配置生效。文件读取并解析成功仅表示该文件通过 YAML 解析，全部合并、端口校验和可选日志配置成功后才输出服务配置生效。

配置只在启动时读取一次，没有动态监听。Token、配置正文和配置值均不输出（最终服务名及端口除外），配置覆盖日志保持原格式。IDL Service 注册是进程内 Kitex 注册，当前没有向 Consul 注册服务实例。准备监听日志不代表已成功绑定端口，实际监听成功以 Kitex 的 `server listen at addr=...` 为准。

Consul 请求使用 `X_CONSUL_ADDR` 明确指定的地址，不使用 SDK 默认地址或其他地址作为回退，也不跟随 HTTP 重定向。日志显示的是请求的端点，当前没有额外查询 Consul 数据中心，也没有按生产或测试名称做环境校验；配置了生产地址就会请求生产地址，不能把日志当作环境隔离机制。

Consul ACL 可让研发只修改入口、由管理员维护共享文件；应用 Token 必须能读取所有引用。ACL 保护共享文件本身，入口仍可覆盖共享字段，覆盖行为遵循上述优先级。

读取并校验成功后，core 把应用服务名和监听端口交给 Kitex，再执行业务注册和启动。当前不增加平台校验、配置热更新或 Consul 实例注册。

后续服务注册与发现使用 `github.com/kitex-contrib/registry-consul`；服务治理后续接入 Sentinel，当前不引入 `config-consul`。官方 Consul SDK 固定为 v1.34.1，Go 最低版本为 1.25.9。

## 注册与包边界

业务处理器由应用创建。WithService 绑定生成的 RegisterService 函数和 Handler，类型、nil 和选项有效性在执行任何注册前校验。所有 Service 按顺序注册到同一个 Server，全部成功后才监听。没有 Service、空回调、空/错误 Handler、重复 Service 或注册失败均会阻止启动。

Server 阻塞运行，返回启动、运行和收尾错误；退出信号和优雅停机由 Kitex 处理。多个 Service 按 IDL Service 名分流，同名方法可以共存。Thrift 客户端需使用 TTHeader 和 transmeta.ClientTTHeaderHandler 传递服务名。

```text
core/
  go.mod
  go.sum
  server.go           # Server、ServerOption
  service.go          # WithService
  logger.go           # WithLogger
  internal/config/    # 环境校验、固定入口及引用读取、配置合并和端口解析
  internal/startup/   # 注册、底层 Kitex 创建与运行
```

根包公开 Server、WithService、WithLogger、WithProbe，以及 ServerOption 和必要的 Probe 接入协议。ServerOption 没有公开字段或方法，零值无效。公开 Handler 参数为 any，兼容注册函数接收 IDL 接口、应用传入具体指针的形式；错误类型会在启动前返回。internal 实现不能由外部业务项目直接导入。

## 显式日志接入

业务通过 `x.WithLogger(logging.New())` 显式选择日志实现。`WithLogger` 接收 Kitex 的 `klog.FullLogger`，同一个 Server 只能传一次，不接受 nil 或带类型的 nil；创建选项不产生全局副作用。应在启动其他日志 goroutine 前调用 Server，Kitex 的 SetLogger 不支持运行期间并发替换。

core 在首次读取 Consul 前安装显式传入的实例，此时使用该实例的默认配置。全部配置合并和端口校验成功后，若实例实现 `ConfigureLogging([]byte) error`，core 单次传入合并后的 logging 节 YAML，不附带 logging 根字段或其他配置；缺省节传 nil。日志配置失败阻止 Service 注册和监听。普通 FullLogger 没有配置接口时，core 不处理 logging 节。

core 在配置失败、注册失败、运行失败或正常退出时都释放显式 logger 的资源；服务停止及最后的结果日志先输出。若实现 `Close() error`，由 Close 负责刷新及关闭；否则若实现 `Sync() error`，则调用 Sync。关闭错误与原错误一起返回。core 不引入 logging、Zap 或 Lumberjack 依赖，也不会通过已安装 logger 或导入包推断是否启用日志接入。

从 xkitex 目录进入独立模块验证：

```sh
cd core
GOWORK=off GOTOOLCHAIN=local go test -race -timeout 45s ./...
GOWORK=off GOTOOLCHAIN=local go build ./...
```

测试使用模拟 Consul HTTP 服务和本机临时端口，不依赖真实 Consul 数据。xkitex 上层目录没有 go.mod、go.work 或 replace。

## 生命周期与可选探针

通过 `x.WithProbe(probe.New())` 显式接入独立 probe 模块。不传时不读取 probe 配置、不启动 HTTP、不等待健康检查；当前依然没有外部服务注册行为，未来注册模块可在原有框架注册位置直接登记为可用。

每个 Server 内独立维护 Starting、Running、Stopping、Stopped、Failed 阶段，以及独立的可用性。Running 仅表示框架运行阶段；接探针时，还需启动检查和就绪检查通过才能 Available。启动完成单向锁存，就绪可失败及恢复。Stopping、Stopped、Failed 始终不可用，不能被迟到的检查或启动通知覆盖。

本实现通过 Kitex 按实例的 Registry 扩展回调观察运行与排空阶段，回调内部不访问注册中心，保留 Kitex 原有约一秒的内部回调时机，没有额外健康等待。没有使用包级全局启动或停机钩子。收到 SIGINT/SIGTERM 时先标记 Stopping，再通过每实例退出通道交给 Kitex 原生排空；探针 HTTP 自身故障标记 Failed 后触发清理。

core 先应用 logging 节，再应用 probe 节；两个回调都只收到各自合并后的 YAML。探针先同步绑定独立 HTTP 端口，再创建及运行 RPC；失败时不继续启动 RPC。正常运行期间，core 只接收后台报告，不通过 HTTP 反向轮询自己。

未来服务注册应消费此可用性：接入探针的实例先登记为不可用，再应用最新检查结果，readiness 恢复时更新同一实例，正常停机时注销。此处没有实现该注册驱动。

本轮生命周期与可选探针能力通过正式 v0.0.1 模块依赖接入。调用样例和 probe 配置说明见相邻 probe 模块的 README。


### 生命周期日志

所有实例生命周期变化通过 Kitex klog 输出，包含已校验的应用服务名、上一阶段和当前阶段。首次记录 `None → Starting`，正常运行记录 `Starting → Running → Stopping → Stopped`，错误路径记录实际进入的 `Failed`；重复通知不重复输出，终止后不接受迟到的运行通知。身份环境变量非法时使用 `service=[未确定]`，不回显非法原值。

```text
core: 实例生命周期变化：service=[zmall-product] from=[None] to=[Starting]
core: 实例生命周期变化：service=[zmall-product] from=[Starting] to=[Running]
core: 实例生命周期变化：service=[zmall-product] from=[Running] to=[Stopping]
core: 实例生命周期变化：service=[zmall-product] from=[Stopping] to=[Stopped]
```

正常 `Stopped` 在 RPC 和 probe 清理完成后、日志资源关闭前输出；后续日志关闭失败会再记录 `Failed`。core 通过可选的 `SetServiceName(string)` 协议将服务名传给 probe，不更改既有 Start 协议，也不向 probe 配置混入其他配置节。既有自定义 Probe 实现不必新增该方法。

startup、readiness、liveness 由 probe 分别记录状态变化及检查项数量，不再只输出 `available`。无 probe 时仍记录实例生命周期，不伪造探针检查结果。
