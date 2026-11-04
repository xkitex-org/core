package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/kitex/server"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// TestBusinessConfigurationSharesMergedSnapshot 验证业务配置独立下发、入口覆盖、各文件只读一次且失败阻止 RPC。
func TestBusinessConfigurationSharesMergedSnapshot(t *testing.T) {
	var reads, callbacks atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		body := "imports: [config/demo/shared.yaml]\nauth: {issuer: application}\ndatabase: {password: private-marker}"
		if strings.HasSuffix(r.URL.Path, "shared.yaml") {
			body = "auth: {issuer: shared, audience: demo}"
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
	}))
	defer consul.Close()
	cfg := NewConfiguration("auth", func(body []byte) error {
		callbacks.Add(1)
		if !strings.Contains(string(body), "application") || !strings.Contains(string(body), "demo") || strings.Contains(string(body), "private-marker") {
			t.Fatal("wrong merged business section")
		}
		return errors.New("private-error-marker")
	})
	created := false
	err := startWithConfiguration(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
		created = true
		return nil
	}, nil, nil, nil, nil, nil, []*Configuration{cfg})
	if err == nil || strings.Contains(err.Error(), "private-error-marker") || created || callbacks.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("configuration contract failed: %v reads=%d callbacks=%d", err, reads.Load(), callbacks.Load())
	}
}

// TestBusinessConfigurationValidation 验证重复与保留节不能覆盖框架接收器，声明阶段不执行回调。
func TestBusinessConfigurationValidation(t *testing.T) {
	called := false
	callback := func([]byte) error { called = true; return nil }
	for _, name := range []string{"", "service", "imports", "logging", "monitor", "database", "cache", "sentinel", "auth.secret"} {
		if _, err := configurationComponents([]*Configuration{NewConfiguration(name, callback)}); err == nil {
			t.Fatal("invalid section accepted")
		}
	}
	valid := NewConfiguration("auth", callback)
	for _, items := range [][]*Configuration{{valid, valid}, {NewConfiguration("auth", nil)}} {
		if _, err := configurationComponents(items); err == nil {
			t.Fatal("invalid callback or duplicate accepted")
		}
	}
	if called {
		t.Fatal("constructor executed callback")
	}
}
