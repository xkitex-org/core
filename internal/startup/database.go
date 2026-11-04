package startup

import (
	"errors"
	"sync"

	"go.chaoxizhongzhi.com/xkitex/database"
)

// Databasing 保存数据库显式选项，不在构造时创建连接池。
type Databasing struct {
	controller database.Controller
	err        error
}

var applicationDatabase = struct {
	sync.RWMutex
	current database.Controller
	idle    database.Controller
}{idle: database.New()}

// NewDatabase 拒绝空实现和带类型的 nil。
func NewDatabase(controller database.Controller) *Databasing {
	if isNil(controller) {
		return &Databasing{err: errors.New("core: database is nil")}
	}
	return &Databasing{controller: controller}
}

// Database 返回当前应用句柄，未接入时返回只会报告未启动错误的空管理器句柄。
func Database() *database.Handle {
	applicationDatabase.RLock()
	defer applicationDatabase.RUnlock()
	if applicationDatabase.current != nil {
		return applicationDatabase.current.Database()
	}
	return applicationDatabase.idle.Database()
}

// bindDatabase 为无参数业务入口保留唯一应用归属，释放时不影响后来启动的应用。
func bindDatabase(controller database.Controller) (func(), error) {
	applicationDatabase.Lock()
	defer applicationDatabase.Unlock()
	if applicationDatabase.current != nil {
		return nil, errors.New("core: 进程中已有正在使用的数据库管理器")
	}
	applicationDatabase.current = controller
	return func() {
		applicationDatabase.Lock()
		defer applicationDatabase.Unlock()
		applicationDatabase.current = nil
	}, nil
}
