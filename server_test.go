package x_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"

	"go.chaoxizhongzhi.com/xkitex/core"
)

type testService interface{ marker() }
type testHandler struct{}

// marker 使测试 Handler 满足测试服务接口。
func (*testHandler) marker() {}

// registerTestService 模拟 Kitex 注册函数，接收服务接口以验证具体 Handler 指针的兼容性。
func registerTestService(s server.Server, handler testService, options ...server.RegisterOption) error {
	return s.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "TestService"}, handler, options...)
}

// TestInvalidOptionsFailBeforeRegistration 验证无效选项会在执行任何注册回调前报错。
func TestInvalidOptionsFailBeforeRegistration(t *testing.T) {
	useConsulConfig(t)
	var nilHandler *testHandler
	var nilRegister func(server.Server, testService, ...server.RegisterOption) error
	for _, tc := range []struct {
		name    string
		options []x.ServerOption
	}{
		{"no services", nil},
		{"zero option", []x.ServerOption{{}}},
		{"nil registration", []x.ServerOption{x.WithService(nilRegister, &testHandler{})}},
		{"nil handler", []x.ServerOption{x.WithService(registerTestService, nil)}},
		{"typed nil handler", []x.ServerOption{x.WithService(registerTestService, nilHandler)}},
		{"wrong handler", []x.ServerOption{x.WithService(registerTestService, struct{}{})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			register := func(s server.Server, handler testService, opts ...server.RegisterOption) error {
				called = true
				return registerTestService(s, handler, opts...)
			}
			options := tc.options
			if len(options) > 0 {
				options = append([]x.ServerOption{x.WithService(register, &testHandler{})}, options...)
			}
			if err := x.Server(options...); err == nil {
				t.Fatal("invalid options were accepted")
			}
			if called {
				t.Fatal("registration ran before all options were validated")
			}
		})
	}
}

// TestRegistrationErrorIsPreservedAndStopsFollowingService 验证注册错误被保留且后续注册停止。
func TestRegistrationErrorIsPreservedAndStopsFollowingService(t *testing.T) {
	useConsulConfig(t)
	want := errors.New("business registration failed")
	called := false
	fail := func(server.Server, testService, ...server.RegisterOption) error { return want }
	next := func(server.Server, testService, ...server.RegisterOption) error { called = true; return nil }
	err := x.Server(x.WithService(fail, &testHandler{}), x.WithService(next, &testHandler{}))
	if !errors.Is(err, want) || called {
		t.Fatalf("registration result: %v, following called: %v", err, called)
	}
}

// TestDuplicateServiceReturnsErrorInsteadOfPanic 验证重复 Service 注册返回错误而非向外抛出 panic。
func TestDuplicateServiceReturnsErrorInsteadOfPanic(t *testing.T) {
	useConsulConfig(t)
	err := x.Server(
		x.WithService(registerTestService, &testHandler{}),
		x.WithService(registerTestService, &testHandler{}),
	)
	if err == nil || !strings.Contains(err.Error(), "already been registered") {
		t.Fatalf("duplicate registration: %v", err)
	}
}

// TestCallbackMustActuallyRegisterAService 验证没有实际注册 Service 的回调不能启动服务。
func TestCallbackMustActuallyRegisterAService(t *testing.T) {
	useConsulConfig(t)
	register := func(server.Server, testService, ...server.RegisterOption) error { return nil }
	if err := x.Server(x.WithService(register, &testHandler{})); err == nil {
		t.Fatal("empty registration callback was accepted")
	}
}

// useConsulConfig 为测试提供固定配置 Key 的模拟 Consul 服务及必要环境变量。
func useConsulConfig(t *testing.T) {
	t.Helper()
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/kv/config/test/api/application.yaml" || r.URL.RawQuery != "" || r.Header.Get("X-Consul-Token") != "test-token" {
			t.Error("unexpected Consul configuration request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("service: {port: 8888}")}})
	}))
	t.Cleanup(consul.Close)
	t.Setenv("X_SERVICE_GROUP", "test")
	t.Setenv("X_SERVICE_NAME", "api")
	t.Setenv("X_CONSUL_ADDR", consul.URL)
	t.Setenv("X_CONSUL_TOKEN", "test-token")
}

// TestMissingIdentityPreventsRegistration 验证缺少服务身份时不会执行注册回调。
func TestMissingIdentityPreventsRegistration(t *testing.T) {
	t.Setenv("X_SERVICE_GROUP", "")
	t.Setenv("X_SERVICE_NAME", "api")
	called := false
	register := func(server.Server, testService, ...server.RegisterOption) error { called = true; return nil }
	err := x.Server(x.WithService(register, &testHandler{}))
	if err == nil || !strings.Contains(err.Error(), "X_SERVICE_GROUP") || called {
		t.Fatalf("missing identity: %v, registration called=%v", err, called)
	}
}
