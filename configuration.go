package x

import "go.chaoxizhongzhi.com/xkitex/core/internal/startup"

// WithConfig 在启动时将合并后的具名业务配置节单次交给 configure，不额外读取或监听 Consul。
// 名称不能与框架保留节重复；回调应只校验配置和构造业务对象，不能保存或输出凭据日志。
func WithConfig(name string, configure func([]byte) error) ServerOption {
	return ServerOption{configuration: startup.NewConfiguration(name, configure)}
}
