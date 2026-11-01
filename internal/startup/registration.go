package startup

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/cloudwego/kitex/server"
)

// Registration 隐藏已绑定的注册回调与构造错误，仅供 core 内部使用。
type Registration struct {
	register func(server.Server) error
	err      error
}

// NewRegistration 保存类型匹配后的 Handler，实际注册延迟到所有选项验证之后。
func NewRegistration[H any](register func(server.Server, H, ...server.RegisterOption) error, handler any) Registration {
	if register == nil {
		return Registration{err: errors.New("registration function is nil")}
	}
	if isNil(handler) {
		return Registration{err: errors.New("handler is nil")}
	}
	typedHandler, ok := handler.(H)
	if !ok {
		return Registration{err: fmt.Errorf("handler %T does not implement registration handler %v", handler, reflect.TypeFor[H]())}
	}
	return Registration{register: func(s server.Server) error { return register(s, typedHandler) }}
}

// isNil 判断普通 nil 和接口中携带的带类型 nil，避免无效 Handler 进入注册阶段。
func isNil(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
