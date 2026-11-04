package startup

import (
	"errors"
	"go.chaoxizhongzhi.com/xkitex/sentinel"
	"sync"
)

// Guarding 保存显式 Sentinel 选项，SDK 和网络任务延迟到启动流程执行。
type Guarding struct {
	controller sentinel.Controller
	err        error
}

var sentinelBinding = struct {
	sync.Mutex
	active bool
}{}

// bindSentinel 在配置和清理前保留唯一运行归属，防止重复复用实例时关闭另一 Server 的保护。
func bindSentinel() (func(), error) {
	sentinelBinding.Lock()
	defer sentinelBinding.Unlock()
	if sentinelBinding.active {
		return nil, errors.New("core: 进程中已有正在使用的 Sentinel 控制器")
	}
	sentinelBinding.active = true
	return func() { sentinelBinding.Lock(); sentinelBinding.active = false; sentinelBinding.Unlock() }, nil
}

// NewSentinel 拒绝 nil 及带类型 nil 的保护实现。
func NewSentinel(controller sentinel.Controller) *Guarding {
	if isNil(controller) {
		return &Guarding{err: errors.New("core: sentinel is nil")}
	}
	return &Guarding{controller: controller}
}
