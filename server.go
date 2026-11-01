package x

import "go.chaoxizhongzhi.com/xkitex/core/internal/startup"

// ServerOption 是由 WithService 创建的服务注册选项，零值无效。
type ServerOption struct {
	registration startup.Registration
}

// Server 校验服务身份并从固定 Consul Key 读取配置后，按顺序注册所有 Service。
// 全部成功后启动同一个 Kitex Server 并阻塞运行，配置、注册或运行失败时返回错误。
// 退出信号和优雅停机由 Kitex 处理。
func Server(options ...ServerOption) error {
	registrations := make([]startup.Registration, len(options))
	for i, option := range options {
		registrations[i] = option.registration
	}
	return startup.Run(registrations...)
}
