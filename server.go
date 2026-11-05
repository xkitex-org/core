package x

import (
	"errors"

	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// ServerOption 是由 WithService、WithLogger、WithMonitor、WithDatabase、WithCache 或 WithSentinel 创建的启动选项，零值无效。
type ServerOption struct {
	registration startup.Registration
	logging      *startup.Logging
	monitor      *startup.Monitoring
	database     *startup.Databasing
	cache        *startup.Caching
	sentinel     *startup.Guarding
}

// Server 校验服务身份并从固定 Consul Key 读取配置后，按顺序注册所有 Service。
// 全部成功后启动同一个 Kitex Server 并阻塞运行，配置、注册或运行失败时返回错误。
// RPC 监听后自动向同一 Consul 登记实例，可选探针决定初始及后续就绪状态。
// 退出时先撤销可用性和注销实例，再由 Kitex 完成优雅排空。
func Server(options ...ServerOption) error {
	registrations := make([]startup.Registration, 0, len(options))
	var logging *startup.Logging
	var monitoring *startup.Monitoring
	var databases *startup.Databasing
	var caches *startup.Caching
	var guarding *startup.Guarding
	for _, option := range options {
		if option.logging != nil {
			if logging != nil {
				return errors.New("core: WithLogger may only be specified once")
			}
			logging = option.logging
		} else if option.monitor != nil {
			if monitoring != nil {
				return errors.New("core: WithMonitor may only be specified once")
			}
			monitoring = option.monitor
		} else if option.database != nil {
			if databases != nil {
				return errors.New("core: WithDatabase may only be specified once")
			}
			databases = option.database
		} else if option.cache != nil {
			if caches != nil {
				return errors.New("core: WithCache may only be specified once")
			}
			caches = option.cache
		} else if option.sentinel != nil {
			if guarding != nil {
				return errors.New("core: WithSentinel may only be specified once")
			}
			guarding = option.sentinel
		} else {
			registrations = append(registrations, option.registration)
		}
	}
	return startup.Run(logging, monitoring, databases, caches, guarding, registrations...)
}
