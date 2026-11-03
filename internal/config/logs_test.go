package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestStartupLogsShowSourceAndSuccessfulStages 验证启动日志按读取、合并、校验顺序输出且不泄露凭据或配置值。
func TestStartupLogsShowSourceAndSuccessfulStages(t *testing.T) {
	output := captureOverrideLogs(t)
	files := map[string]string{
		testApplicationKey:              "imports: [config/zmall/shared/base.yaml]\nservice: {port: 9088}\ndatabase: {password: private-application-password}",
		"config/zmall/shared/base.yaml": "service: {port: 9001}\ndatabase: {host: private-database-host}",
	}
	settings, err := loadFilesThroughConsul(t, files)
	if err != nil || settings.Port != 9088 {
		t.Fatalf("startup failed: %+v %v", settings, err)
	}
	logs := output.String()
	stages := []string{
		"启动配置来源：service=[zmall-product] consul=[http://127.0.0.1:",
		"entry=[config/zmall/product/application.yaml]",
		"开始读取配置文件：key=[config/zmall/product/application.yaml]",
		"配置文件读取并解析成功：key=[config/zmall/product/application.yaml]",
		"开始读取配置文件：key=[config/zmall/shared/base.yaml]",
		"配置文件读取并解析成功：key=[config/zmall/shared/base.yaml]",
		"配置文件合并完成：files=[2] priority=[application.yaml 优先]",
		"配置[service.port]已被[config/zmall/product/application.yaml]覆盖。",
		"配置加载完成：mode=[启动时读取一次，不监听变更] elapsed=[",
		"服务配置生效：name=[zmall-product] port=[9088] port_source=[config/zmall/product/application.yaml]",
	}
	previous := -1
	for _, stage := range stages {
		index := strings.Index(logs, stage)
		if index <= previous || strings.Count(logs, stage) != 1 {
			t.Fatalf("stage missing, duplicated or out of order: %s", stage)
		}
		previous = index
	}
	for _, private := range []string{"test-token", "private-application-password", "private-database-host"} {
		if strings.Contains(logs, private) {
			t.Fatal("startup logs exposed a credential or configuration value")
		}
	}
}

// TestPortLogsIdentifyDefaultAndImportedSources 验证日志区分默认端口及引用文件提供的端口。
func TestPortLogsIdentifyDefaultAndImportedSources(t *testing.T) {
	for _, tc := range []struct {
		name, application, fragment, want string
	}{
		{"default", "{}", "", "port=[8888] port_source=[默认值]"},
		{"imported", "imports: [config/zmall/shared/base.yaml]", "service: {port: 9088}", "port=[9088] port_source=[config/zmall/shared/base.yaml]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOverrideLogs(t)
			_, err := loadFilesThroughConsul(t, map[string]string{testApplicationKey: tc.application, "config/zmall/shared/base.yaml": tc.fragment})
			if err != nil || !strings.Contains(output.String(), tc.want) {
				t.Fatalf("missing effective port source: %v", err)
			}
		})
	}
}

// TestFailedLoadLogsStageWithoutSuccessOrSecrets 验证读取、解析或最终校验失败时明确报错，不误报生效且不泄露响应正文。
func TestFailedLoadLogsStageWithoutSuccessOrSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		parsed bool
	}{
		{"denied", http.StatusForbidden, "private-response-body", false},
		{"invalid YAML", http.StatusOK, "private-configuration-value: [", false},
		{"invalid port", http.StatusOK, "service: {port: private-port-value}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOverrideLogs(t)
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(tc.body)}})
				} else {
					_, _ = w.Write([]byte(tc.body))
				}
			}))
			defer consul.Close()
			_, err := Load(context.Background(), lookup(environment(consul.URL)))
			logs := output.String()
			if err == nil || !strings.Contains(logs, "配置加载失败：") || !strings.Contains(logs, testApplicationKey) {
				t.Fatal("failed load did not report its configuration key")
			}
			if strings.Contains(logs, "配置加载完成：") || strings.Contains(logs, "服务配置生效：") || strings.Contains(logs, "private-") || strings.Contains(logs, "test-token") {
				t.Fatal("failed load reported success or exposed private values")
			}
			if strings.Contains(logs, "配置文件读取并解析成功：") != tc.parsed {
				t.Fatal("parse stage was reported incorrectly")
			}
		})
	}
}

// TestLoggedEndpointMatchesRequestsWithoutFallback 验证日志地址即真实请求目标，SDK 默认地址及重定向均不能切换目标。
func TestLoggedEndpointMatchesRequestsWithoutFallback(t *testing.T) {
	var otherCalls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { otherCalls.Add(1) }))
	defer other.Close()
	t.Setenv("CONSUL_HTTP_ADDR", other.URL)
	t.Setenv("CONSUL_HTTP_SSL", "true")
	for _, redirect := range []bool{false, true} {
		output := captureOverrideLogs(t)
		var calls atomic.Int32
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if redirect {
				http.Redirect(w, r, other.URL, http.StatusFound)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("{}")}})
		}))
		_, err := Load(context.Background(), lookup(environment(consul.URL)))
		consul.Close()
		if (err != nil) != redirect || calls.Load() != 1 || otherCalls.Load() != 0 {
			t.Fatal("configuration reader changed endpoint or retried elsewhere")
		}
		if !strings.Contains(output.String(), "consul=["+consul.URL+"]") {
			t.Fatal("logged Consul address does not match actual request target")
		}
	}
}
