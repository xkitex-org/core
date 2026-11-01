package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/pkg/klog"
)

// captureOverrideLogs 捕获默认 Kitex klog 输出，并在测试结束后恢复 stderr。
func captureOverrideLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	klog.SetOutput(&output)
	t.Cleanup(func() { klog.SetOutput(os.Stderr) })
	return &output
}

// loadFilesThroughConsul 通过模拟 Consul 执行真实配置加载，缺少的 Key 返回 404。
func loadFilesThroughConsul(t *testing.T, files map[string]string) (Settings, error) {
	t.Helper()
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/v1/kv/")
		body, exists := files[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
	}))
	defer consul.Close()
	return Load(context.Background(), lookup(environment(consul.URL)))
}

// overrideMessages 提取 klog 消息正文，使断言不依赖日志时间和调用位置。
func overrideMessages(output string) []string {
	var messages []string
	for _, line := range strings.Split(output, "\n") {
		if index := strings.Index(line, "配置["); index >= 0 {
			messages = append(messages, line[index:])
		}
	}
	return messages
}

// TestOverrideLogsOnlyReportFinalEffectiveFile 验证十个引用反复覆盖时，每个字段只输出最终文件且不泄露值。
func TestOverrideLogsOnlyReportFinalEffectiveFile(t *testing.T) {
	output := captureOverrideLogs(t)
	files := make(map[string]string)
	var application strings.Builder
	application.WriteString("imports:\n")
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("config/zmall/shared/import%d.yaml", i)
		fmt.Fprintf(&application, "  - %s\n", key)
		files[key] = fmt.Sprintf("database: {postgres: {host: secret-value-%d, port: 5432}}\nservice: {port: %d}", i, 9000+i)
	}
	files["config/zmall/shared/import0.yaml"] += "\nunique: secret-value-only-once"
	application.WriteString("database: {postgres: {host: secret-value-application}}\nservice: {port: 9088}")
	files[testApplicationKey] = application.String()
	settings, err := loadFilesThroughConsul(t, files)
	if err != nil || settings.Port != 9088 {
		t.Fatalf("startup failed: %+v %v", settings, err)
	}
	want := []string{
		"配置[database.postgres.host]已被[config/zmall/product/application.yaml]覆盖。",
		"配置[database.postgres.port]已被[config/zmall/shared/import9.yaml]覆盖。",
		"配置[service.port]已被[config/zmall/product/application.yaml]覆盖。",
	}
	if got := overrideMessages(output.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("override messages=%v, want %v", got, want)
	}
	if strings.Contains(output.String(), "secret-value") {
		t.Fatal("configuration values leaked into logs")
	}
}

// TestOverrideLogsReflectFinalStructure 验证整体替换清除失效子字段日志，列表和 null 只记录自身字段。
func TestOverrideLogsReflectFinalStructure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		application string
		first       string
		second      string
		fields      []string
	}{
		{"object replaced", "database: disabled", "database: {host: first}", "database: {host: second}", []string{"database"}},
		{"list replaced", "features: [application]", "features: [first]", "features: [second]", []string{"features"}},
		{"null replaced", "database: null", "database: {host: first}", "database: {host: second}", []string{"database"}},
		{"scalar replaced by object", "database: {host: application}", "database: disabled", "database: {host: second}", []string{"database.host"}},
		{"new fields only", "database: {username: application}", "database: {host: first}", "database: {port: 5432}", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOverrideLogs(t)
			files := map[string]string{
				testApplicationKey:                "imports: [config/zmall/shared/first.yaml, config/zmall/shared/second.yaml]\n" + tc.application,
				"config/zmall/shared/first.yaml":  tc.first,
				"config/zmall/shared/second.yaml": tc.second,
			}
			if _, err := loadFilesThroughConsul(t, files); err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, field := range tc.fields {
				want = append(want, fmt.Sprintf("配置[%s]已被[%s]覆盖。", field, testApplicationKey))
			}
			if got := overrideMessages(output.String()); !reflect.DeepEqual(got, want) {
				t.Fatalf("override messages=%v, want %v", got, want)
			}
		})
	}
}

// TestFailedConfigurationDoesNotLogEffectiveOverrides 验证读取或最终校验失败时不输出生效覆盖日志。
func TestFailedConfigurationDoesNotLogEffectiveOverrides(t *testing.T) {
	for _, tc := range []struct {
		name        string
		application string
		fragment    string
	}{
		{"unreadable import", "service: {port: 9088}\nimports: [config/zmall/shared/base.yaml, config/zmall/shared/missing.yaml]", "service: {port: 9001}"},
		{"invalid final port", "service: {port: 0}\nimports: [config/zmall/shared/base.yaml]", "service: {port: 9001}"},
		{"invalid fragment", "service: {port: 9088}\nimports: [config/zmall/shared/base.yaml]", "service: ["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOverrideLogs(t)
			settings, err := loadFilesThroughConsul(t, map[string]string{
				testApplicationKey:              tc.application,
				"config/zmall/shared/base.yaml": tc.fragment,
			})
			if err == nil || settings != (Settings{}) || len(overrideMessages(output.String())) != 0 {
				t.Fatalf("failed configuration reported effective overrides: err=%v logs=%s", err, output.String())
			}
		})
	}
}
