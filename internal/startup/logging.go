package startup

import (
	"errors"
	"io"

	"github.com/cloudwego/kitex/pkg/klog"
)

// Logging 保存显式日志选项；core 不通过包引入或当前全局 logger 推断是否启用。
type Logging struct {
	logger klog.FullLogger
	err    error
}

// loggingConfigurer 约定只接收合并后的 logging 节 YAML，nil 表示该节缺省。
type loggingConfigurer interface {
	ConfigureLogging([]byte) error
}

// loggingSyncer 兼容仅提供刷新能力、没有关闭方法的业务 logger。
type loggingSyncer interface {
	Sync() error
}

// NewLogging 保存日志选项并拒绝普通 nil 及带类型的 nil，实现校验不产生全局副作用。
func NewLogging(logger klog.FullLogger) *Logging {
	if isNil(logger) {
		return &Logging{err: errors.New("core: logger is nil")}
	}
	return &Logging{logger: logger}
}

// configure 返回可选配置回调；普通 FullLogger 不读取或校验 logging 节。
func (l *Logging) configure() func([]byte) error {
	if configurable, ok := l.logger.(loggingConfigurer); ok {
		return configurable.ConfigureLogging
	}
	return nil
}

// close 优先调用负责刷新并关闭资源的 Close，否则仅调用可选 Sync，不处理未传入的 logger。
func (l *Logging) close() error {
	if closer, ok := l.logger.(io.Closer); ok {
		return closer.Close()
	}
	if syncer, ok := l.logger.(loggingSyncer); ok {
		return syncer.Sync()
	}
	return nil
}
