package startup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// Run 先校验身份并读取固定 Consul 配置，成功后才创建、注册和运行 Server。
func Run(logging *Logging, monitoring *Monitoring, databases *Databasing, caches *Caching, guarding *Guarding, configurations []*Configuration, registrations ...Registration) error {
	create := serverFactory(func(ctx context.Context, settings config.Settings, state *lifecycle.State, exit <-chan error) server.Server {
		options := middlewareOptions(monitoring, guarding)
		return managedServerWithOptions(ctx, settings, state, exit, options...)
	})
	return startWithConfiguration(context.Background(), os.LookupEnv, create, logging, monitoring, databases, caches, guarding, configurations, registrations...)
}

// middlewareOptions 保证监控包在治理外层，使被限流或熔断拒绝的调用也被计数。
func middlewareOptions(monitoring *Monitoring, guarding *Guarding) []server.Option {
	var options []server.Option
	if monitoring != nil {
		if instrumented, ok := monitoring.controller.(interface{ Middleware() endpoint.Middleware }); ok {
			options = append(options, server.WithMiddleware(instrumented.Middleware()))
		}
	}
	if guarding != nil {
		options = append(options, server.WithMiddleware(guarding.controller.Middleware()))
	}
	return options
}

// start 先加载启动配置，再使用该配置创建 Server 并执行服务注册和启动。
func start(ctx context.Context, lookup func(string) (string, bool), create func(config.Settings) server.Server, registrations ...Registration) error {
	return startWithLogging(ctx, lookup, create, nil, registrations...)
}

// startWithLogging 按显式选项安装 logger，配置成功后运行服务，并在最后一条退出日志后释放日志资源。
func startWithLogging(ctx context.Context, lookup func(string) (string, bool), create func(config.Settings) server.Server, logging *Logging, registrations ...Registration) error {
	return startWithComponents(ctx, lookup, func(_ context.Context, settings config.Settings, _ *lifecycle.State, _ <-chan error) server.Server {
		return create(settings)
	}, logging, nil, registrations...)
}

// startWithComponents 保留不接入数据库时的独立启动流程。
func startWithComponents(ctx context.Context, lookup func(string) (string, bool), create serverFactory, logging *Logging, monitoring *Monitoring, registrations ...Registration) error {
	return startWithDatabase(ctx, lookup, create, logging, monitoring, nil, registrations...)
}

// startWithDatabase 保留既有仅接入数据库的启动路径。
func startWithDatabase(ctx context.Context, lookup func(string) (string, bool), create serverFactory, logging *Logging, monitoring *Monitoring, databases *Databasing, registrations ...Registration) error {
	return startWithResources(ctx, lookup, create, logging, monitoring, databases, nil, registrations...)
}

// startWithResources 保留未启用 Sentinel 的数据源启动流程。
func startWithResources(ctx context.Context, lookup func(string) (string, bool), create serverFactory, logging *Logging, monitoring *Monitoring, databases *Databasing, caches *Caching, registrations ...Registration) error {
	return startWithSentinel(ctx, lookup, create, logging, monitoring, databases, caches, nil, registrations...)
}

// startWithSentinel 管理普通配置、数据源、规则监听、探针和 RPC，退出时先排空 RPC 再取消规则监听。
func startWithSentinel(ctx context.Context, lookup func(string) (string, bool), create serverFactory, logging *Logging, monitoring *Monitoring, databases *Databasing, caches *Caching, guarding *Guarding, registrations ...Registration) (err error) {
	return startWithConfiguration(ctx, lookup, create, logging, monitoring, databases, caches, guarding, nil, registrations...)
}

// startWithConfiguration 将业务配置接到同一次配置加载中，再依序启动连接池、监控、治理和 RPC。
func startWithConfiguration(ctx context.Context, lookup func(string) (string, bool), create serverFactory, logging *Logging, monitoring *Monitoring, databases *Databasing, caches *Caching, guarding *Guarding, configurations []*Configuration, registrations ...Registration) (err error) {
	var configure func([]byte) error
	var state *lifecycle.State
	if logging != nil {
		if logging.err != nil {
			return logging.err
		}
		klog.SetLogger(logging.logger)
		defer func() {
			if closeErr := logging.close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("core: close logger: %w", closeErr))
				state.Finish(err)
			}
		}()
		configure = logging.configure()
	}
	serviceName, _ := config.ServiceName(lookup)
	state = lifecycle.New(serviceName, monitoring != nil)
	defer func() { state.Finish(err) }()
	if guarding != nil {
		if guarding.err != nil {
			return guarding.err
		}
		release, bindErr := bindSentinel()
		if bindErr != nil {
			return bindErr
		}
		defer release()
	}
	components, configErr := configurationComponents(configurations)
	if configErr != nil {
		return configErr
	}
	if databases != nil {
		if databases.err != nil {
			return databases.err
		}
		release, bindErr := bindDatabase(databases.controller)
		if bindErr != nil {
			return bindErr
		}
		defer release()
		// 此 defer 先于探针清理登记，确保先停探针、再关连接池；RPC 已在 runWithState 内排空。
		defer func() { err = errors.Join(err, databases.controller.Close()) }()
		components = append(components, config.Component{Name: "database", Configure: databases.controller.ConfigureDatabase})
	}
	if caches != nil {
		if caches.err != nil {
			return caches.err
		}
		release, bindErr := bindCache(caches.controller)
		if bindErr != nil {
			return bindErr
		}
		defer release()
		defer func() { err = errors.Join(err, caches.controller.Close()) }()
		components = append(components, config.Component{Name: "cache", Configure: caches.controller.ConfigureCache})
	}
	if guarding != nil {
		components = append(components, config.Component{Name: "sentinel", Configure: guarding.controller.ConfigureSentinel})
	}
	if monitoring != nil && monitoring.err != nil {
		return monitoring.err
	}
	var configureMonitor func([]byte) error
	if monitoring != nil {
		if named, ok := monitoring.controller.(interface{ SetServiceName(string) }); ok {
			named.SetServiceName(serviceName)
		}
		configureMonitor = monitoring.controller.ConfigureMonitor
		defer func() {
			if err != nil {
				state.Finish(err)
			} else {
				state.Stop()
			}
			err = errors.Join(err, monitoring.controller.Close())
		}()
	}
	var settings config.Settings
	defer func() {
		if guarding != nil {
			err = errors.Join(err, guarding.controller.Close())
		}
		settings.CloseConsul()
	}()
	settings, err = config.LoadWithComponents(ctx, lookup, configure, configureMonitor, components...)
	if err != nil {
		return err
	}
	if databases != nil {
		if err := startDependency(ctx, "database", databases.controller, monitoring); err != nil {
			return err
		}
	}
	if caches != nil {
		if err := startDependency(ctx, "cache", caches.controller, monitoring); err != nil {
			return err
		}
	}
	runtimeCtx, exit, watch, cleanup := supervise(ctx, state)
	defer cleanup()
	if guarding != nil {
		if err := guarding.controller.Start(runtimeCtx, settings.ConsulClient(), settings.ServiceName); err != nil {
			return fmt.Errorf("core: start sentinel: %w", err)
		}
	}
	var fatal <-chan error
	if monitoring != nil {
		fatal, err = monitoring.controller.Start(runtimeCtx, state.Phase, func(started, ready bool) { reportMonitorHealth(state, started, ready) })
		if err != nil {
			return fmt.Errorf("core: start monitor: %w", err)
		}
	}
	watch(fatal)
	klog.Infof("core: 准备启动服务：name=[%s] listen=[:%d]", settings.ServiceName, settings.Port)
	err = runWithState(func() server.Server { return create(runtimeCtx, settings, state, exit) }, state, registrations...)
	if err != nil {
		klog.Errorf("core: 服务启动或运行失败：name=[%s] error=[%v]", settings.ServiceName, err)
	} else {
		klog.Infof("core: 服务已停止：name=[%s]", settings.ServiceName)
	}
	return err
}

// newServer 配置应用服务名、监听端口及按 IDL Service 名分流的 Kitex Server。
func newServer(settings config.Settings, options ...server.Option) server.Server {
	defaults := []server.Option{
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: settings.ServiceName}),
		server.WithServiceAddr(&net.TCPAddr{Port: settings.Port}),
		server.WithCompatibleMiddlewareForUnary(),
		// 同名方法按 IDL Service 名分流，拒绝缺少服务名的歧义调用。
		server.WithRefuseTrafficWithoutServiceName(),
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
	}
	return server.NewServer(append(defaults, options...)...)
}

// run 校验全部注册项，按顺序注册到同一 Server，并在运行结束后停止 Server。
func run(create func() server.Server, registrations ...Registration) (err error) {
	state := lifecycle.New("", false)
	defer func() { state.Finish(err) }()
	return runWithState(create, state, registrations...)
}

// runWithState 先校验并注册全部业务服务，终止状态在 RPC 清理前生效且不可被后台检查覆盖。
func runWithState(create func() server.Server, state *lifecycle.State, registrations ...Registration) (err error) {
	if len(registrations) == 0 {
		return errors.New("core: at least one Service is required")
	}
	// 所有选项先校验，避免后面的无效 Handler 触发部分注册。
	for i, registration := range registrations {
		if registration.err != nil {
			return fmt.Errorf("core: service %d: %w", i+1, registration.err)
		}
		if registration.register == nil {
			return fmt.Errorf("core: service %d: missing registration", i+1)
		}
	}
	s := create()
	defer func() { state.Stop(); err = errors.Join(err, s.Stop()) }()
	for i, registration := range registrations {
		if err := registerService(s, registration.register); err != nil {
			return fmt.Errorf("core: register service %d: %w", i+1, err)
		}
	}
	infos := s.GetServiceInfos()
	if len(infos) == 0 {
		return errors.New("core: no Service was registered")
	}
	services := make([]string, 0, len(infos))
	for name := range infos {
		services = append(services, name)
	}
	sort.Strings(services)
	klog.Infof("core: IDL Service 注册完成：services=[%s]", strings.Join(services, ", "))
	return s.Run()
}

// registerService 执行单个注册回调，并将注册阶段的 panic 转为错误。
func registerService(s server.Server, register func(server.Server) error) (err error) {
	// Kitex 对重复 Service 等注册错误会 panic，限制在注册阶段转换为启动错误。
	defer func() {
		if failure := recover(); failure != nil {
			if cause, ok := failure.(error); ok {
				err = fmt.Errorf("registration panicked: %w", cause)
			} else {
				err = fmt.Errorf("registration panicked: %v", failure)
			}
		}
	}()
	return register(s)
}
