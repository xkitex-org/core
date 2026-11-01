package config

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// environment 构造包含必需启动参数的测试环境。
func environment(address string) map[string]string {
	return map[string]string{
		"X_SERVICE_GROUP": "zmall", "X_SERVICE_NAME": "product",
		"X_CONSUL_ADDR": address, "X_CONSUL_TOKEN": "test-token",
	}
}

// lookup 将测试环境映射适配为环境变量读取函数。
func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

// TestFixedKeyIdentityAndConfiguredPort 验证固定 Key、ACL Token、派生服务名及单次端口读取。
func TestFixedKeyIdentityAndConfiguredPort(t *testing.T) {
	var calls atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/kv/config/zmall/product/application.yaml" || r.URL.RawQuery != "" || r.Header.Get("X-Consul-Token") != "test-token" {
			t.Error("incorrect Consul key, query, method or authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("service: {name: cannot-override-identity, port: 9088}\ndatabase: {postgres: {host: internal}}\n")}})
	}))
	defer consul.Close()
	values := environment(strings.TrimPrefix(consul.URL, "http://"))
	values["X_SERVICE_GROUP"], values["X_SERVICE_NAME"] = " zmall ", " product "
	values["X_CONFIG_KEY"] = "legacy/arbitrary/config.yaml"
	settings, err := Load(context.Background(), lookup(values))
	if err != nil || settings.ServiceName != "zmall-product" || settings.Port != 9088 || calls.Load() != 1 {
		t.Fatalf("settings=%+v err=%v calls=%d", settings, err, calls.Load())
	}
}

// TestPortDefaultOnlyWhenOmitted 验证仅缺省端口使用默认值，非法配置均被拒绝。
func TestPortDefaultOnlyWhenOmitted(t *testing.T) {
	for _, raw := range []string{"{}", "service: {}", "database: {host: internal}", "service: {name: ignored}"} {
		if port, err := documentPort([]byte(raw)); err != nil || port != 8888 {
			t.Fatalf("default port for %q: %d %v", raw, port, err)
		}
	}
	for _, raw := range []string{"service: {port: 1}", "service: {port: 65535}"} {
		if _, err := documentPort([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		"", "null", "[]", "a: [", "{}\n---\n{}", "{}\n---",
		"service: null", "service: []", "service: invalid",
		"service: {port: 0}", "service: {port: -1}", "service: {port: 65536}",
		"service: {port: null}", "service: {port: false}", "service: {port: 8888.0}",
		"service: {port: '8888'}", "service: {port: secret-value}",
		"service: {port: 8888, port: 9999}", "service: {}\nservice: {}",
	} {
		if _, err := documentPort([]byte(raw)); err == nil {
			t.Fatalf("invalid configuration was accepted: %q", raw)
		} else if strings.Contains(err.Error(), "secret-value") {
			t.Fatal("configuration value leaked in error")
		}
	}
}

// TestRequiredEnvironmentAndPathValidationBeforeHTTP 验证环境变量和路径校验失败时不发起请求。
func TestRequiredEnvironmentAndPathValidationBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer consul.Close()
	for _, key := range []string{"X_SERVICE_GROUP", "X_SERVICE_NAME", "X_CONSUL_ADDR", "X_CONSUL_TOKEN"} {
		for _, missing := range []string{"", "  "} {
			values := environment(consul.URL)
			values[key] = missing
			if _, err := Load(context.Background(), lookup(values)); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("%s missing but accepted: %v", key, err)
			}
		}
	}
	for _, key := range []string{"X_SERVICE_GROUP", "X_SERVICE_NAME"} {
		for _, value := range []string{"../other", "other/app", `other\app`, ".", "..", "bad name", "bad?key", "bad\x00name"} {
			values := environment(consul.URL)
			values[key] = value
			if _, err := Load(context.Background(), lookup(values)); err == nil {
				t.Fatalf("invalid %s accepted", key)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid bootstrap values caused an HTTP request")
	}
}

// TestConsulAddressValidation 验证 Consul 地址格式及错误信息的凭据脱敏。
func TestConsulAddressValidation(t *testing.T) {
	for _, address := range []string{"http://127.0.0.1:8500", "127.0.0.1:8500", "https://consul.internal:8501/"} {
		if _, err := consulEndpoint(lookup(environment(address))); err != nil {
			t.Fatalf("valid Consul address rejected: %v", err)
		}
	}
	for _, address := range []string{"ftp://host:8500", "http://", "http://host/config", "http://host?x=y", "http://host/#fragment", "http://user:secret-value@host"} {
		if _, err := consulEndpoint(lookup(environment(address))); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid address accepted or exposed: %v", err)
		}
	}
}

// TestConsulFailuresDoNotReturnPartialOrDefaultConfiguration 验证读取失败不返回配置或泄露响应正文。
func TestConsulFailuresDoNotReturnPartialOrDefaultConfiguration(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/another/path")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("secret-value"))
			}))
			defer consul.Close()
			settings, err := Load(context.Background(), lookup(environment(consul.URL)))
			if err == nil || settings != (Settings{}) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("failed Consul request returned configuration or leaked body: %+v %v", settings, err)
			}
		})
	}
	for _, body := range []string{"", "service: {port: invalid-secret}", strings.Repeat("x", maxConfigBytes+1)} {
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
		}))
		settings, err := Load(context.Background(), lookup(environment(consul.URL)))
		consul.Close()
		if err == nil || settings != (Settings{}) || strings.Contains(err.Error(), "invalid-secret") {
			t.Fatalf("bad configuration returned partial settings: %+v %v", settings, err)
		}
	}
}

// TestCancelledConsulReadFails 验证读取遵守上下文期限并保留超时错误。
func TestCancelledConsulReadFails(t *testing.T) {
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer consul.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	settings, err := Load(ctx, lookup(environment(consul.URL)))
	if !errors.Is(err, context.DeadlineExceeded) || settings != (Settings{}) {
		t.Fatalf("Consul read ignored deadline: %+v %v", settings, err)
	}
}

// TestInvalidSDKResponsesFailWithoutExposingValues 验证空结果及非法 SDK 响应均报错且不泄露值。
func TestInvalidSDKResponsesFailWithoutExposingValues(t *testing.T) {
	for _, body := range []string{
		`[]`, `null`,
		`[{"Value":"not-valid-base64-secret-value"}]`,
		`[{"Value":{},"Flags":"secret-value"}]`,
		`{"secret-value":true}`,
	} {
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		settings, err := Load(context.Background(), lookup(environment(consul.URL)))
		consul.Close()
		if err == nil || settings != (Settings{}) || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid SDK response accepted or exposed: %+v %v", settings, err)
		}
	}
}

// documentPort 解析测试文档并提取端口，覆盖 YAML 校验及最终端口校验。
func documentPort(body []byte) (int, error) {
	document, err := decodeDocument(body)
	if err != nil {
		return 0, err
	}
	return parsePort(document)
}

// TestLoadReadsImportsOnceWithSameACL 验证固定入口及引用均单次读取，使用同一 ACL 且以入口端口为准。
func TestLoadReadsImportsOnceWithSameACL(t *testing.T) {
	files := map[string]string{
		"config/zmall/product/application.yaml": "imports: [config/zmall/shared/database.yaml, config/zmall/shared/redis.yaml]\nservice: {port: 9088}",
		"config/zmall/shared/database.yaml":     "database: {postgres: {host: shared}}\nservice: {port: 9001}",
		"config/zmall/shared/redis.yaml":        "database: {redis: {address: shared}}",
	}
	var mu sync.Mutex
	var calls []string
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/v1/kv/")
		mu.Lock()
		calls = append(calls, key)
		mu.Unlock()
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("X-Consul-Token") != "test-token" {
			t.Error("imports changed authentication or used a blocking/list/write request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, exists := files[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
	}))
	defer consul.Close()
	settings, err := Load(context.Background(), lookup(environment(consul.URL)))
	if err != nil || settings.ServiceName != "zmall-product" || settings.Port != 9088 {
		t.Fatalf("imported startup failed: settings=%+v err=%v", settings, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != "config/zmall/product/application.yaml,config/zmall/shared/database.yaml,config/zmall/shared/redis.yaml" {
		t.Fatalf("unexpected imports requests: %v", calls)
	}
}

// TestUnreadableImportPreventsFallback 验证引用缺失或无权限时，即使入口字段完整也不能跳过引用启动。
func TestUnreadableImportPreventsFallback(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/kv/config/zmall/product/application.yaml" {
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("imports: [config/zmall/shared/database.yaml]\nservice: {port: 9088}")}})
					return
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("secret-value"))
			}))
			defer consul.Close()
			settings, err := Load(context.Background(), lookup(environment(consul.URL)))
			if err == nil || settings != (Settings{}) || strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("unreadable import accepted or exposed: %+v %v", settings, err)
			}
		})
	}
}
