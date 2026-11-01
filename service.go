package x

import (
	kitexserver "github.com/cloudwego/kitex/server"

	"go.chaoxizhongzhi.com/xkitex/core/internal/startup"
)

// WithService 绑定 Kitex 生成的 RegisterService 函数和业务 Handler，暂不执行注册。
// H 从注册函数推导，Handler 在 Server 启动前检查类型及 nil 值；错误由 Server 返回。
func WithService[H any](
	register func(kitexserver.Server, H, ...kitexserver.RegisterOption) error,
	handler any,
) ServerOption {
	return ServerOption{registration: startup.NewRegistration(register, handler)}
}
