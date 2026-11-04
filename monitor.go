package x

import (
	"context"
	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// Monitor 是可替换的监控接入协议；配置仅包含 monitor 节，阶段和结果均按当前 Server 隔离。
// Start 的 state 返回 Starting、Running、Stopping、Stopped 或 Failed；report 报告启动完成和就绪结果。
// Start 返回的错误通道只报告探针服务自身的致命错误，检查失败不得作为通道错误退出应用。
// 可选实现 Middleware() endpoint.Middleware 时，core 会在 Sentinel 外层安装 RPC 指标中间件。
type Monitor interface {
	// ConfigureMonitor 接收单次合并后的 monitor 配置节。
	ConfigureMonitor([]byte) error
	// Start 启动监控资源，报告启动与就绪结果；错误通道只用于监控服务自身致命故障。
	Start(context.Context, func() string, func(started, ready bool)) (<-chan error, error)
	// Close 在 RPC 排空后取消后台任务并释放 HTTP 监听，允许重复调用。
	Close() error
}

// WithMonitor 显式启用监控出口；未传选项时，不启动 HTTP 服务、不等待健康检查。
func WithMonitor(controller Monitor) ServerOption {
	return ServerOption{monitor: startup.NewMonitor(controller)}
}
