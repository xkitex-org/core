package x_test

import (
	"strings"
	"testing"

	"github.com/cloudwego/kitex/pkg/klog"

	"go.chaoxizhongzhi.com/xkitex/core"
)

// nilLogger 通过嵌入接口构造带类型 nil，以验证 WithLogger 不执行其任何方法。
type nilLogger struct{ klog.FullLogger }

// TestInvalidLoggerOptionsHaveNoGlobalEffects 验证重复及 nil 日志选项在加载配置前失败，且不修改全局 logger。
func TestInvalidLoggerOptionsHaveNoGlobalEffects(t *testing.T) {
	previous := klog.DefaultLogger()
	var typedNil *nilLogger
	for _, options := range [][]x.ServerOption{
		{x.WithLogger(nil)},
		{x.WithLogger(typedNil)},
		{x.WithLogger(previous), x.WithLogger(previous)},
	} {
		err := x.Server(options...)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "logger") || klog.DefaultLogger() != previous {
			t.Fatalf("invalid logger option changed global state: %v", err)
		}
	}
}
