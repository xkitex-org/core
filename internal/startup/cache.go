package startup

import (
	"errors"
	"sync"

	"go.chaoxizhongzhi.com/xkitex/cache"
)

// Caching 保存显式缓存选项及校验结果，不在选项创建时分配连接池。
type Caching struct {
	controller cache.Controller
	err        error
}

var applicationCache = struct {
	sync.RWMutex
	current cache.Controller
	idle    cache.Controller
}{idle: cache.New()}

// NewCache 拒绝 nil 及带类型 nil 的实现。
func NewCache(controller cache.Controller) *Caching {
	if isNil(controller) {
		return &Caching{err: errors.New("core: cache is nil")}
	}
	return &Caching{controller: controller}
}

// Cache 获取当前应用句柄，未接入时返回只报告生命周期错误的句柄。
func Cache() *cache.Handle {
	applicationCache.RLock()
	defer applicationCache.RUnlock()
	if applicationCache.current != nil {
		return applicationCache.current.Cache()
	}
	return applicationCache.idle.Cache()
}

// bindCache 为无参数入口保留进程内唯一归属，重复启动不会覆盖已有客户端。
func bindCache(controller cache.Controller) (func(), error) {
	applicationCache.Lock()
	defer applicationCache.Unlock()
	if applicationCache.current != nil {
		return nil, errors.New("core: 进程中已有正在使用的缓存管理器")
	}
	applicationCache.current = controller
	return func() {
		applicationCache.Lock()
		defer applicationCache.Unlock()
		applicationCache.current = nil
	}, nil
}
