package x_test

import (
	"context"
	"go.chaoxizhongzhi.com/xkitex/core"
	"strings"
	"testing"
)

// nilMonitor 提供探针协议以检查带类型 nil，不启动任何资源。
type nilMonitor struct{}

// ConfigureMonitor 是测试替身，不执行配置。
func (*nilMonitor) ConfigureMonitor([]byte) error { return nil }

// Start 是测试替身，不启动服务。
func (*nilMonitor) Start(context.Context, func() string, func(bool, bool)) (<-chan error, error) {
	return nil, nil
}

// Close 是测试替身，不持有资源。
func (*nilMonitor) Close() error { return nil }

// TestInvalidMonitorOptionsFailBeforeConfiguration 验证空值及重复选项在网络或资源副作用前被拒绝。
func TestInvalidMonitorOptionsFailBeforeConfiguration(t *testing.T) {
	var typed *nilMonitor
	for _, options := range [][]x.ServerOption{{x.WithMonitor(nil)}, {x.WithMonitor(typed)}, {x.WithMonitor(&nilMonitor{}), x.WithMonitor(&nilMonitor{})}} {
		if err := x.Server(options...); err == nil || !strings.Contains(strings.ToLower(err.Error()), "monitor") {
			t.Fatalf("invalid monitor option: %v", err)
		}
	}
}
