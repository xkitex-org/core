package x_test

import (
	"context"
	"strings"
	"testing"

	"go.chaoxizhongzhi.com/xkitex/core"
	"go.chaoxizhongzhi.com/xkitex/database"
)

// nilDatabase 复用接口方法集，只验证带类型 nil 的选项校验。
type nilDatabase struct{ database.Controller }

// TestInvalidDatabaseOptionsFailBeforeConfiguration 验证空值及重复显式选项在配置读取前被拒绝。
func TestInvalidDatabaseOptionsFailBeforeConfiguration(t *testing.T) {
	var typed *nilDatabase
	for _, options := range [][]x.ServerOption{
		{x.WithDatabase(nil)}, {x.WithDatabase(typed)}, {x.WithDatabase(database.New()), x.WithDatabase(database.New())},
	} {
		if err := x.Server(options...); err == nil || !strings.Contains(strings.ToLower(err.Error()), "database") {
			t.Fatalf("invalid database option: %v", err)
		}
	}
	if _, err := x.Database().Context(context.Background()).Exec("SELECT 1"); err == nil {
		t.Fatal("database started without explicit option")
	}
}
