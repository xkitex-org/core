package x

import (
	"errors"

	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// ServerOption 是由 WithService、WithLogger 或 WithProbe 创建的启动选项，零值无效。
type ServerOption struct {
	registration startup.Registration
	logging      *startup.Logging
	probe        *startup.Probing
}

// Server 校验服务身份并从固定 Consul Key 读取配置后，按顺序注册所有 Service。
// 全部成功后启动同一个 Kitex Server 并阻塞运行，配置、注册或运行失败时返回错误。
// RPC 监听后自动向同一 Consul 登记实例，可选探针决定初始及后续就绪状态。
// 退出时先撤销可用性和注销实例，再由 Kitex 完成优雅排空。
func Server(options ...ServerOption) error {
	registrations := make([]startup.Registration, 0, len(options))
	var logging *startup.Logging
	var probing *startup.Probing
	for _, option := range options {
		if option.logging != nil {
			if logging != nil {
				return errors.New("core: WithLogger may only be specified once")
			}
			logging = option.logging
		} else if option.probe != nil {
			if probing != nil {
				return errors.New("core: WithProbe may only be specified once")
			}
			probing = option.probe
		} else {
			registrations = append(registrations, option.registration)
		}
	}
	return startup.Run(logging, probing, registrations...)
}
