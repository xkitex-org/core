package startup

import (
	"context"
	"errors"
)

// MonitorController 只使用标准库和基础类型，允许独立公共包通过结构化接口接入。
type MonitorController interface {
	// ConfigureMonitor 接收单次合并后的 monitor 配置节。
	ConfigureMonitor([]byte) error
	// Start 启动监控资源，报告启动与就绪结果；错误通道只用于监控服务自身致命故障。
	Start(context.Context, func() string, func(bool, bool)) (<-chan error, error)
	// Close 在 RPC 排空后取消后台任务并释放 HTTP 监听，允许重复调用。
	Close() error
}

// Monitoring 保存显式选项及其校验结果，不在选项构造阶段启动资源。
type Monitoring struct {
	controller MonitorController
	err        error
}

// NewMonitor 拒绝普通 nil 和带类型 nil 的探针实现。
func NewMonitor(controller MonitorController) *Monitoring {
	if isNil(controller) {
		return &Monitoring{err: errors.New("core: monitor is nil")}
	}
	return &Monitoring{controller: controller}
}
