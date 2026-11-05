package startup

import (
	"errors"
	"os"
	"sync"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

var configurationMu sync.Mutex
var sharedConfiguration = config.NewStore()
var configurationRunning bool

// Config 返回当前启动轮次的统一视图，创建视图不触发网络请求。
func Config() *config.View {
	configurationMu.Lock()
	defer configurationMu.Unlock()
	return config.NewView(sharedConfiguration, os.LookupEnv)
}

// claimConfiguration 将业务预读与本轮 Server 绑定，阻止进程内并发覆盖应用资源。
func claimConfiguration() (*config.Store, func(), error) {
	configurationMu.Lock()
	defer configurationMu.Unlock()
	if configurationRunning {
		return nil, nil, errors.New("core: Server 已在运行")
	}
	configurationRunning = true
	store := sharedConfiguration
	return store, func() {
		configurationMu.Lock()
		defer configurationMu.Unlock()
		sharedConfiguration = config.NewStore()
		configurationRunning = false
	}, nil
}
