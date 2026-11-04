package x

import (
	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
	"go.chaoxizhongzhi.com/xkitex/sentinel"
)

// WithSentinel 显式启用入站保护，基础配置只读一次，规则从同一 Consul 动态更新。
// 未传选项时不读取 sentinel 节、不初始化 SDK、不创建规则监听或中间件。
func WithSentinel(controller sentinel.Controller) ServerOption {
	return ServerOption{sentinel: startup.NewSentinel(controller)}
}
