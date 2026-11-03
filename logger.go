package x

import (
	"github.com/cloudwego/kitex/pkg/klog"

	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// WithLogger 指定应用使用的 Kitex logger；未传此选项时，core 不修改现有日志实现。
// logger 在读取 Consul 前安装；可选的配置和关闭接口由 core 在启动及退出时调用。
func WithLogger(logger klog.FullLogger) ServerOption {
	return ServerOption{logging: startup.NewLogging(logger)}
}
