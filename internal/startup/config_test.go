package startup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

// configEnvironment 提供启动流程测试所需的服务身份及 Consul 连接参数。
func configEnvironment(address string) func(string) (string, bool) {
	values := map[string]string{"X_SERVICE_GROUP": "demo", "X_SERVICE_NAME": "api", "X_CONSUL_ADDR": address, "X_CONSUL_TOKEN": "test-token"}
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

// TestConfigurationIsLoadedBeforeServerAndRegistration 验证配置读取早于 Server 创建和 Service 注册。
func TestConfigurationIsLoadedBeforeServerAndRegistration(t *testing.T) {
	var fetched atomic.Bool
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kv/config/demo/api/application.yaml" {
			t.Error("wrong configuration key")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fetched.Store(true)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("service: {port: 9088}")}})
	}))
	defer consul.Close()
	s := &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	registered := false
	registration := NewRegistration(func(actual server.Server, _ *int, _ ...server.RegisterOption) error {
		registered = true
		return actual.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "IDLService"}, new(int))
	}, new(int))
	err := start(context.Background(), configEnvironment(consul.URL), func(settings config.Settings) server.Server {
		if !fetched.Load() || settings.ServiceName != "demo-api" || settings.Port != 9088 {
			t.Fatalf("server created without valid configuration: %+v", settings)
		}
		return s
	}, registration)
	if err != nil || !registered || s.runs != 1 {
		t.Fatalf("configured startup failed: %v", err)
	}
}

// TestConfigurationFailurePreventsCreationAndRegistration 验证非法配置会阻止 Server 创建和 Service 注册。
func TestConfigurationFailurePreventsCreationAndRegistration(t *testing.T) {
	for _, body := range []string{"service: {port: 0}", "not: [valid"} {
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
		}))
		created, registered := false, false
		registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { registered = true; return nil }, new(int))
		err := start(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server {
			created = true
			return &fakeServer{}
		}, registration)
		consul.Close()
		if err == nil || created || registered {
			t.Fatalf("bad configuration reached startup: %v created=%v registered=%v", err, created, registered)
		}
	}
}

// TestImportedConfigurationFailurePreventsStartup 验证引用读取、解析或最终端口校验失败时不会创建和注册服务。
func TestImportedConfigurationFailurePreventsStartup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"forbidden", http.StatusForbidden, ""},
		{"missing", http.StatusNotFound, ""},
		{"invalid YAML", http.StatusOK, "invalid: ["},
		{"invalid port", http.StatusOK, "service: {port: 0}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/kv/config/demo/api/application.yaml" {
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("imports: [config/demo/shared/base.yaml]")}})
					return
				}
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(tc.body)}})
				}
			}))
			defer consul.Close()
			created, registered := false, false
			registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { registered = true; return nil }, new(int))
			err := start(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server {
				created = true
				return &fakeServer{}
			}, registration)
			if err == nil || created || registered {
				t.Fatalf("failed imports reached startup: err=%v created=%v registered=%v", err, created, registered)
			}
		})
	}
}
