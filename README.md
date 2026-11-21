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
  sources:
    primary:
      host: application-database.internal
```

例如 `config/zmall/shared/database.yaml` 的内容为：

```yaml
database:
  default: primary
  sources:
    primary:
      type: postgresql
      host: shared-database.internal
      port: 5432
      database: zmall
      username: application
```

最终数据库配置的 host 为 `application-database.internal`，port 和 username 保留共享值。core 直接使用合并后的 `service.port`；显式传入的日志、探针和数据库实现分别接收 `logging`、`monitor`、`database` 节。其余配置节参与加载和合并，当前不增加业务配置的公共读取接口。

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
配置[database.sources.primary.host]已被[config/zmall/product/application.yaml]覆盖。
```

未传 `WithLogger` 时，core 不修改现有 klog 日志实现、输出或级别，也不调用其配置、关闭或刷新方法。

启动期间同样使用 Kitex `klog` 输出以下信息：校验后的实际 Consul 协议和地址、派生服务名、固定入口 Key，每个入口或引用文件开始读取及 YAML 解析成功、文件合并数量、最终端口及其生效文件（缺省时标记默认值），随后记录准备监听的地址及已注册的 IDL Service 名称。配置加载失败使用 Error 日志记录安全错误，并终止启动；失败时不输出服务配置生效。文件读取并解析成功仅表示该文件通过 YAML 解析，全部合并、端口校验和可选日志配置成功后才输出服务配置生效。

配置只在启动时读取一次，没有动态监听。Token、配置正文和配置值均不输出（最终服务名及端口除外），配置覆盖日志保持原格式。IDL Service 注册是进程内 Kitex 注册；监听成功后，core 自动向 Consul 登记一个应用实例。准备监听日志不代表已成功绑定端口，实际监听成功以 Kitex 的 `server listen at addr=...` 为准。

Consul 请求使用 `X_CONSUL_ADDR` 明确指定的地址，不使用 SDK 默认地址或其他地址作为回退，也不跟随 HTTP 重定向。日志显示的是请求的端点，当前没有额外查询 Consul 数据中心，也没有按生产或测试名称做环境校验；配置了生产地址就会请求生产地址，不能把日志当作环境隔离机制。

Consul ACL 可让研发只修改入口、由管理员维护共享文件；应用 Token 必须能读取所有引用。ACL 保护共享文件本身，入口仍可覆盖共享字段，覆盖行为遵循上述优先级。

读取并校验成功后，core 把应用服务名和监听端口交给 Kitex，再执行业务注册和启动。当前不增加平台校验或配置热更新。

服务注册使用 `github.com/kitex-contrib/registry-consul` v0.2.0，客户端发现接入继续延后；服务治理后续接入 Sentinel，当前不引入 `config-consul`。官方 Consul SDK 固定为 v1.34.1，Go 最低版本为 1.25.9。

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

根包公开 Server、WithService、WithLogger、WithMonitor、WithDatabase、Database、WithCache、Cache、WithSentinel、Config，以及 ServerOption、Configuration 和必要的 Monitor 接入协议。ServerOption 没有公开字段或方法，零值无效。公开 Handler 参数为 any，兼容注册函数接收 IDL 接口、应用传入具体指针的形式；错误类型会在启动前返回。internal 实现不能由外部业务项目直接导入。

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

通过 `x.WithMonitor(monitor.New())` 显式接入独立 monitor 模块。不传时不读取 monitor 配置、不启动 HTTP、不等待健康检查；监听成功后自动登记应用实例，首次登记即 passing。

每个 Server 内独立维护 Starting、Running、Stopping、Stopped、Failed 阶段，以及独立的可用性。Running 仅表示框架运行阶段；接探针时，还需启动检查和就绪检查通过才能 Available。启动完成单向锁存，就绪可失败及恢复。Stopping、Stopped、Failed 始终不可用，不能被迟到的检查或启动通知覆盖。

本实现通过 Kitex 按实例的 Registry 扩展回调登记和注销实例，登记成功后记录 Running，保留 Kitex 原有约一秒的内部回调时机；未接探针时不增加健康等待。没有使用包级全局启动或停机钩子。收到 SIGINT/SIGTERM 时先标记 Stopping，再通过每实例退出通道交给 Kitex 原生排空；探针 HTTP 自身故障标记 Failed 后触发清理。

core 先应用 logging 节，再应用 monitor 节；两个回调都只收到各自合并后的 YAML。探针先同步绑定独立 HTTP 端口，再创建及运行 RPC；失败时不继续启动 RPC。正常运行期间，core 只接收后台报告，不通过 HTTP 反向轮询自己。

接入探针的实例首次登记即不可用，startup 完成且 readiness 通过后更新独立就绪检查，readiness 失败和恢复更新同一实例；正常停机先撤销可用性再注销。

本轮生命周期与可选探针能力通过正式 v0.0.1 模块依赖接入。调用样例和 monitor 配置说明见相邻 monitor 模块的 README。


### 生命周期日志

所有实例生命周期变化通过 Kitex klog 输出，包含已校验的应用服务名、上一阶段和当前阶段。首次记录 `None → Starting`，正常运行记录 `Starting → Running → Stopping → Stopped`，错误路径记录实际进入的 `Failed`；重复通知不重复输出，终止后不接受迟到的运行通知。身份环境变量非法时使用 `service=[未确定]`，不回显非法原值。

```text
core: 实例生命周期变化：service=[zmall-product] from=[None] to=[Starting]
core: 实例生命周期变化：service=[zmall-product] from=[Starting] to=[Running]
core: 实例生命周期变化：service=[zmall-product] from=[Running] to=[Stopping]
core: 实例生命周期变化：service=[zmall-product] from=[Stopping] to=[Stopped]
```

正常 `Stopped` 在 RPC 和 monitor 清理完成后、日志资源关闭前输出；后续日志关闭失败会再记录 `Failed`。core 通过可选的 `SetServiceName(string)` 协议将服务名传给 monitor，不更改既有 Start 协议，也不向 monitor 配置混入其他配置节。既有自定义 Monitor 实现不必新增该方法。

startup、readiness、liveness 由 monitor 分别记录状态变化及检查项数量，不再只输出 `available`。无 monitor 时仍记录实例生命周期，不伪造探针检查结果。


## Consul 实例注册

注册是 core 的内置能力，业务无需新增选项或环境变量。沿用读取应用配置时已经校验的 Consul 地址及 ACL Token，不重新读取环境或回退到其他端点。应用 Token 需要配置读取权限及目标服务的 `service:write` 权限；只有 KV 读取权限时，登记失败会阻止启动。

```go
// 不接探针：RPC 监听成功后直接登记为可用。
x.Server(
    x.WithService(productservice.RegisterService, productHandler),
    x.WithService(categoryservice.RegisterService, categoryHandler),
)

// 接探针：先登记为不可用，业务就绪后再放行。
x.Server(
    x.WithMonitor(monitor.New(monitor.WithReadiness("database", checkDatabase))),
    x.WithService(productservice.RegisterService, productHandler),
    x.WithService(categoryservice.RegisterService, categoryHandler),
)
```

一个 Server 在 Consul 中登记一个 `GROUP-NAME` 应用实例，多个 IDL Service 共用它。实例 ID 沿用 registry-consul 的 `服务名:IP:RPC端口` 格式，登记 RPC 端口而非 HTTP 探针端口。通配监听地址会选择本机非回环 IPv4，日志输出实际登记地址；当前不增加广告地址配置，调用方必须能够访问该地址。

| 条件 | Consul 服务检查 |
| --- | --- |
| 未监听或配置失败 | 不登记 |
| 未接 monitor | 首次登记即 passing |
| 接入 monitor，startup/readiness 未通过 | 首次登记即 critical |
| startup 完成且 readiness 通过 | 独立 readiness 检查变为 passing |
| readiness 失败或恢复 | 同一检查在 critical/passing 间切换 |
| 正常停机 | 停止续约、进入维护状态、注销，再排空 RPC |
| 进程无法续约 | TTL 到期变为 critical，保留记录 |

registry-consul 负责最终登记、注销及实例心跳；SDK 在首次登记时原子创建初始检查。心跳与 readiness 使用不同的 TTL 检查 ID，每次启动生成新的检查 ID，旧心跳不能更新新检查。接 monitor 时，SDK 创建 critical 的 readiness 检查，后续第三方登记保留它，不存在先可用再撤销的初始窗口。空的 `monitor.New()` 没有业务检查，进入 Running 后会很快通过；liveness 不直接控制 Consul 就绪状态。

TTL 默认 15 秒，core 每 5 秒补充续约；状态变化立即唤醒同步任务。任务按实例串行处理并读取最新状态，探针不等待网络请求。同步失败后重试，远端成功才输出就绪同步成功日志，不把本地检查通过当作 Consul 已经更新。Agent 丢失登记时先重建初始检查再应用最新状态；不会因 readiness 长期失败自动删除实例。

实例带有 `xkitex.managed` 和 `xkitex.owner` 标签。core 独占管理该实例的检查集合，启动时原子替换本实例遗留检查，避免上一次接 monitor、本次不接而被旧检查卡住；非 core 管理的同 ID 实例会拒绝覆盖。不得在 core 管理的实例上额外挂载需要跨重启保留的检查；其他实例不受影响。

正常停机先锁定不可恢复的 Stopping 状态，再启用 Consul 维护模式，防止第三方心跳重新放行，随后由 registry-consul 注销。未确认注销成功时返回错误，远端租约会在停止续约后到期。所有请求有超时，第三方错误响应正文不进入日志。registry-consul v0.2.0 没有独立 Close，且只在注销成功后取消内部心跳；内部适配在真实清理后停止外部请求，并仅在本地完成其取消流程。这个本地收尾结果不代表远端注销成功，也不会覆盖真实错误。

本轮只接入服务注册，没有增加客户端封装、管理平台校验或服务治理。注册实现位于 `internal/registration`，根包没有新增公开方法。

验证包含模拟 Agent、真实 Kitex 多 Service 调用，以及本机临时 Consul 的 ACL/TTL 测试；真实 Agent 测试只使用随机端口和临时目录，不访问现有环境。本机未安装 consul 时，对应测试会明确跳过。


## 独立数据库模块

通过 `x.WithDatabase(database.New())` 显式启用 `go.chaoxizhongzhi.com/xkitex/database`。core 在启动时单次传入合并后的 database 节，支持 default 与 sources 具名配置，驱动类型为 postgresql 或 mysql。业务在请求期间通过 `x.Database().Context(ctx)` 查询，通过 `WithDataSource(name)` 选择独立数据源。未启用、未启动或已关闭时执行操作会返回错误。

```go
err := x.Server(
    x.WithDatabase(database.New()),
    x.WithMonitor(monitor.New()),
    x.WithService(productservice.RegisterService, productHandler),
)
```

`x.Database().Begin(ctx)` 返回携带事务的新 ctx、事务控制对象和错误。Service 显式提交，Repository 只传 ctx，继续调用 `x.Database().Context(ctx)` 即可复用事务。结束事务、跨源、跨管理器和嵌套开启均被拒绝，不回退普通连接。查询及事务接口的实现位于独立 database 模块；core 不直接操作 XORM。

无参数入口只绑定进程中一套应用数据源。不要在启动前或包级变量中缓存句柄；绑定随 Server 退出释放。未传 WithDatabase 的旧用法保持不变，不读取额外配置、不创建数据库资源。

接入 monitor 时自动添加逐源 readiness Ping，全部通过后沿用现有 Consul 可用性更新逻辑；无 monitor 不增加 Ping 或健康等待。自定义 Monitor 需实现可选的 AddReadiness 接口才能自动添加依赖，缺少接口时明确报错。RPC 排空后先停止 monitor，再回滚未结束事务、关闭连接池，最后结束生命周期并关闭日志。

本次没有发布新版本。新增 database 的正式模块路径保留在 go.mod；本地验证通过 database/scripts/test-local.py 临时模块代理读取源码快照，不使用 go.work 或 replace。完整配置、调用样例与验证边界见相邻 database/README.md。


## 独立缓存模块

通过 `x.WithCache(cache.New())` 显式启用 `go.chaoxizhongzhi.com/xkitex/cache`，与 database 可以分别或同时接入。core 从同一份启动合并配置中单次下发 cache 节，支持明确的 default 和具名 sources；类型为 redis 或 valkey，每个数据源对应独立单节点连接池。

```go
err := x.Server(
    x.WithDatabase(database.New()),
    x.WithCache(cache.New()),
    x.WithMonitor(monitor.New()),
    x.WithService(productservice.RegisterService, productHandler),
)
```

业务直接调用 `x.Cache().Get(ctx, key)`、`Set` 和管道命令，通过 `WithDataSource(name)` 选择其他数据源。缓存命令沿用 go-redis 类型和 redis.Nil，ctx 不把缓存操作加入 SQL 事务。未启用、未启动或已关闭均不连接默认服务器；未知具名数据源返回错误。进程内缓存入口与应用一同绑定、释放，不允许重复覆盖。

同时接 monitor 时自动挂载 cache.NAME 就绪检查，与 database.NAME 独立命名，全部通过才就绪。没有 monitor 时不额外 Ping。core 共用依赖启动与检查挂载流程，资源在 RPC 排空、monitor 关闭后按启动逆序释放：cache、database。自定义 monitor 缺少 AddReadiness 时明确拒绝启动，不悄悄漏掉依赖检查。

缓存配置和调用样例见相邻 cache/README.md；本次保持本地工作稿，未发布。跨模块验证复用 database/scripts/test-local.py 的临时模块代理，不添加 go.work 或 replace。


## Sentinel 动态保护

通过 `x.WithSentinel(sentinel.New())` 显式接入独立模块 `go.chaoxizhongzhi.com/xkitex/sentinel`。core 仅在启动时传入合并后的 sentinel 节，并把已经校验的同一 Consul 客户端与 GROUP-NAME 服务名交给模块。普通配置只读一次；只有 rule_prefix 下的 flow、circuitBreaker、isolation、system 四个 JSON 数组 Key 动态更新。

```go
err := x.Server(
    x.WithDatabase(database.New()),
    x.WithCache(cache.New()),
    x.WithSentinel(sentinel.New()),
    x.WithMonitor(monitor.New()),
    x.WithService(productservice.RegisterService, productHandler),
)
```

默认为 `config/<group>/<name>/sentinel`，与主配置 `config/<group>/<name>/application.yaml` 同目录。可省略 `sentinel` 配置节。已有规则使用其他目录时，可用 `sentinel.rule_prefix` 覆盖默认值：

```yaml
sentinel:
  rule_prefix: config/zmall/shared/sentinel
```

四个 Key 都必须存在，没有规则时写入 []；初始读取或校验失败阻止 RPC 启动。运行中网络错误、ACL 故障、删除 Key 和非法内容保留上一版有效规则，明确写 [] 才清空该类保护。规则监听不改变普通配置或既有 monitor 就绪依据。

真实 Kitex Server 创建时安装入站中间件，资源名为应用名/IDL Service/方法；同名方法不串资源。业务错误进入 SDK 熔断统计，panic 释放 Entry 并继续传播。停机先取消规则请求的运行上下文，已加载规则保持到 RPC 排空；之后等待监听退出并清理规则，再释放 Consul 客户端、monitor 和连接池。

SDK 的进程级采集与规则约束、初始数据及完整测试说明见相邻 sentinel/README.md。没有增加 SDK 文件日志、额外 HTTP 端口或环境变量配置覆盖；模块诊断统一走 klog。未启用 WithSentinel 时不处理该节或创建规则监听。当前版本为 v0.0.1，验证通过 database/scripts/test-local.py 的临时模块代理完成。


## monitor 迁移

原 probe 模块改为 `go.chaoxizhongzhi.com/xkitex/monitor`，显式接入改为 `x.WithMonitor(monitor.New())`，配置根改为 monitor。保留 `/live`、`/ready`、`/startup`，同端口新增 `/info`、`/metrics`，暴露当前应用进程和 Go 运行时数据，不采集宿主机。默认端口为 9091。database/cache 自动就绪检查和 Consul 就绪状态联动保持一致。

原生 monitor 提供 RPC 指标中间件，core 将其放在 Sentinel 外层，使被拒绝的调用也计入错误。自定义 Monitor 可只实现基础健康协议，或额外实现 `Middleware() endpoint.Middleware` 接入 RPC 观测。未接入 monitor 时不启动 HTTP 或采样、不安装观测中间件。监控模块和 core 无直接模块依赖。

该版本与 monitor、database、cache、sentinel 一起发布为 v0.0.1，业务使用正式模块依赖。


## 统一业务配置

`x.Config().Decode(&cfg)` 由 core 统一读取固定 Consul 入口和 imports，完成覆盖合并后映射到业务配置类型。构造配置视图不做 I/O；首次 Decode 或 Server 加载成功后，本轮启动复用同一只读快照，不重新读取 Consul。启动结束后，新一轮 Server 使用新快照。

业务配置类型和校验集中在项目根目录 config；Handler、Service、Repository 不解析 YAML，不访问 Consul。Decode 返回独立对象，对象修改不会影响公共包或其他调用者。结构体只接收其声明的根配置节，并严格校验节内字段。

业务先完成配置校验和对象构造，再调用 Server；公共包使用同一快照初始化。配置加载日志暂存到 logger 生效后按原顺序输出，仍统一使用 klog。身份在预读后变化会阻止启动。旧的 WithConfig 节名回调接口已删除。

此配置 API 随本次 core v0.0.1 正式发布，业务通过正式模块依赖使用，不写 go.work 或 replace。
