package x_test

import (
	"strings"
	"testing"

	"go.chaoxizhongzhi.com/xkitex/core"
	"go.chaoxizhongzhi.com/xkitex/sentinel"
)

// nilSentinel 提供带类型 nil 的接口方法集。
type nilSentinel struct{ sentinel.Controller }

// TestInvalidSentinelOptionsFailBeforeConfiguration 验证空值及重复选项不会读取 Consul 或初始化 SDK。
func TestInvalidSentinelOptionsFailBeforeConfiguration(t *testing.T) {
	var typed *nilSentinel
	for _, options := range [][]x.ServerOption{{x.WithSentinel(nil)}, {x.WithSentinel(typed)}, {x.WithSentinel(sentinel.New()), x.WithSentinel(sentinel.New())}} {
		if err := x.Server(options...); err == nil || !strings.Contains(strings.ToLower(err.Error()), "sentinel") {
			t.Fatalf("invalid sentinel option: %v", err)
		}
	}
}
