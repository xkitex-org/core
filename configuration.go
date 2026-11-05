package x

import "go.chaoxizhongzhi.com/xkitex/core/internal/startup"

// Configuration 是统一配置的只读入口，Decode 不暴露或修改 core 内部快照。
type Configuration interface {
	// Decode 首次使用时由 core 加载并合并 Consul 配置，后续调用和 Server 复用同一快照。
	Decode(any) error
}

// Config 返回进程内统一配置视图；仅保存声明不会产生网络访问，不提供动态更新入口。
func Config() Configuration { return startup.Config() }
