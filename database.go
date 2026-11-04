package x

import (
	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
	"go.chaoxizhongzhi.com/xkitex/database"
)

// WithDatabase 显式接入独立数据库模块，配置在启动时单次下发，资源在 RPC 排空后关闭。
// 无数据库选项时不读取 database 节，也不创建连接池或健康检查。
func WithDatabase(controller database.Controller) ServerOption {
	return ServerOption{database: startup.NewDatabase(controller)}
}

// Database 返回当前应用的默认数据源句柄；需要调用 Context(ctx) 才能执行查询。
// 使用 WithDataSource 取得具名数据源，Begin 返回用于跨业务方法传播事务的新 ctx。
// 当前进程只允许一套显式接入的数据源管理器；启动前或关闭后的操作均返回错误。
func Database() *database.Handle { return startup.Database() }
