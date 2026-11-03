package startup

import (
	"context"
	"errors"
)

// ProbeController 只使用标准库和基础类型，允许独立公共包通过结构化接口接入。
type ProbeController interface {
	ConfigureProbe([]byte) error
	Start(context.Context, func() string, func(bool, bool)) (<-chan error, error)
	Close() error
}

// Probing 保存显式选项及其校验结果，不在选项构造阶段启动资源。
type Probing struct {
	controller ProbeController
	err        error
}

// NewProbe 拒绝普通 nil 和带类型 nil 的探针实现。
func NewProbe(controller ProbeController) *Probing {
	if isNil(controller) {
		return &Probing{err: errors.New("core: probe is nil")}
	}
	return &Probing{controller: controller}
}
