package x

import (
	"go.chaoxizhongzhi.com/xkitex/cache"
	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// WithCache 显式启用独立缓存模块，配置只在启动时加载，连接池在 RPC 排空后关闭。
func WithCache(controller cache.Controller) ServerOption {
	return ServerOption{cache: startup.NewCache(controller)}
}

// Cache 返回当前应用默认数据源的 go-redis 命令句柄，命令显式接收业务 ctx。
// WithDataSource 可获取具名句柄；未知名称、未启用及已关闭均不回退默认服务器。
func Cache() *cache.Handle { return startup.Cache() }
