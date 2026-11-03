package x_test

import (
	"context"
	"go.chaoxizhongzhi.com/xkitex/core"
	"strings"
	"testing"
)

// nilProbe 提供探针协议以检查带类型 nil，不启动任何资源。
type nilProbe struct{}

// ConfigureProbe 是测试替身，不执行配置。
func (*nilProbe) ConfigureProbe([]byte) error { return nil }

// Start 是测试替身，不启动服务。
func (*nilProbe) Start(context.Context, func() string, func(bool, bool)) (<-chan error, error) {
	return nil, nil
}

// Close 是测试替身，不持有资源。
func (*nilProbe) Close() error { return nil }

// TestInvalidProbeOptionsFailBeforeConfiguration 验证空值及重复选项在网络或资源副作用前被拒绝。
func TestInvalidProbeOptionsFailBeforeConfiguration(t *testing.T) {
	var typed *nilProbe
	for _, options := range [][]x.ServerOption{{x.WithProbe(nil)}, {x.WithProbe(typed)}, {x.WithProbe(&nilProbe{}), x.WithProbe(&nilProbe{})}} {
		if err := x.Server(options...); err == nil || !strings.Contains(strings.ToLower(err.Error()), "probe") {
			t.Fatalf("invalid probe option: %v", err)
		}
	}
}
