package x

import (
	"context"
	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// Probe 是可替换的探针接入协议；配置仅包含 probe 节，阶段和结果均按当前 Server 隔离。
// Start 的 state 返回 Starting、Running、Stopping、Stopped 或 Failed；report 报告启动完成和就绪结果。
// Start 返回的错误通道只报告探针服务自身的致命错误，检查失败不得作为通道错误退出应用。
type Probe interface {
	ConfigureProbe([]byte) error
	Start(context.Context, func() string, func(started, ready bool)) (<-chan error, error)
	Close() error
}

// WithProbe 显式启用探针控制；未传选项时，不启动 HTTP 服务、不等待健康检查。
func WithProbe(controller Probe) ServerOption {
	return ServerOption{probe: startup.NewProbe(controller)}
}
