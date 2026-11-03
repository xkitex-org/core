package startup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

// Run 先校验身份并读取固定 Consul 配置，成功后才创建、注册和运行 Server。
func Run(registrations ...Registration) error {
	return start(context.Background(), os.LookupEnv, func(settings config.Settings) server.Server { return newServer(settings) }, registrations...)
}

// start 先加载启动配置，再使用该配置创建 Server 并执行服务注册和启动。
func start(ctx context.Context, lookup func(string) (string, bool), create func(config.Settings) server.Server, registrations ...Registration) error {
	settings, err := config.Load(ctx, lookup)
	if err != nil {
		return err
	}
	klog.Infof("core: 准备启动服务：name=[%s] listen=[:%d]", settings.ServiceName, settings.Port)
	err = run(func() server.Server { return create(settings) }, registrations...)
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
	defer func() { err = errors.Join(err, s.Stop()) }()
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
