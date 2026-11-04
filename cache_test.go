package x_test

import (
	"context"
	"strings"
	"testing"

	"go.chaoxizhongzhi.com/xkitex/cache"
	"go.chaoxizhongzhi.com/xkitex/core"
)

// nilCache 提供缓存接口方法集以验证带类型 nil 的选项检查。
type nilCache struct{ cache.Controller }

// TestInvalidCacheOptionsFailBeforeConfiguration 验证空实现和重复选项在配置读取前被拒绝。
func TestInvalidCacheOptionsFailBeforeConfiguration(t *testing.T) {
	var typed *nilCache
	for _, options := range [][]x.ServerOption{{x.WithCache(nil)}, {x.WithCache(typed)}, {x.WithCache(cache.New()), x.WithCache(cache.New())}} {
		if err := x.Server(options...); err == nil || !strings.Contains(strings.ToLower(err.Error()), "cache") {
			t.Fatalf("invalid cache option: %v", err)
		}
	}
	if err := x.Cache().Get(context.Background(), "key").Err(); err == nil {
		t.Fatal("cache started without explicit option")
	}
}
