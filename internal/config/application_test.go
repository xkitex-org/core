package config

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const testApplicationKey = "config/zmall/product/application.yaml"

// loadTestFiles 使用内存配置验证完整的入口加载和合并，并记录读取顺序。
func loadTestFiles(files map[string]string) (map[string]any, []string, error) {
	var calls []string
	application, err := loadApplication(context.Background(), testApplicationKey, func(_ context.Context, key string) ([]byte, error) {
		calls = append(calls, key)
		body, exists := files[key]
		if !exists {
			return nil, fmt.Errorf("missing test configuration %q", key)
		}
		return []byte(body), nil
	})
	if err != nil {
		return nil, calls, err
	}
	return application.values, calls, nil
}

// TestApplicationOverridesImports 验证入口优先、引用顺序、嵌套字段保留及列表和 null 的整体替换。
func TestApplicationOverridesImports(t *testing.T) {
	files := map[string]string{
		testApplicationKey: `
imports:
  - config/zmall/shared/first.yaml
  - config/zmall/shared/second.yaml
database:
  postgres:
    host: application-host
features: [application]
nullable: null
shape: {application: true}
service: {port: 9088}
`,
		"config/zmall/shared/first.yaml": `
database:
  postgres: {host: first-host, port: 5432}
  redis: {address: first-redis}
features: [first]
nullable: fallback
shape: scalar
shared: {value: first}
service: {port: 0}
`,
		"config/zmall/shared/second.yaml": `
database:
  postgres: {host: second-host, username: application-user}
features: [second]
shape: {second: true}
shared: {value: second}
service: {port: 9001}
`,
	}
	application, calls, err := loadTestFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{
		"database": map[string]any{
			"postgres": map[string]any{"host": "application-host", "port": 5432, "username": "application-user"},
			"redis":    map[string]any{"address": "first-redis"},
		},
		"features": []any{"application"},
		"nullable": nil,
		"shape":    map[string]any{"second": true, "application": true},
		"shared":   map[string]any{"value": "second"},
		"service":  map[string]any{"port": 9088},
	}
	if !reflect.DeepEqual(application, expected) {
		t.Fatalf("merged configuration differs: got %#v", application)
	}
	if !reflect.DeepEqual(calls, []string{testApplicationKey, "config/zmall/shared/first.yaml", "config/zmall/shared/second.yaml"}) {
		t.Fatalf("unexpected read order or duplicate reads: %v", calls)
	}
}

// TestPortIsValidatedAfterMerging 验证端口来自最终合并结果，入口非法值不会回退到引用值。
func TestPortIsValidatedAfterMerging(t *testing.T) {
	for _, tc := range []struct {
		name     string
		local    string
		imported string
		want     int
	}{
		{"imported port", "", "service: {port: 9001}", 9001},
		{"local port", "service: {port: 9088}", "service: {port: 0}", 9088},
		{"local invalid port", "service: {port: 0}", "service: {port: 9001}", 0},
		{"local null port", "service: {port: null}", "service: {port: 9001}", 0},
		{"local invalid service", "service: disabled", "service: {port: 9001}", 0},
		{"default port", "", "database: {host: internal}", 8888},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application, _, err := loadTestFiles(map[string]string{
				testApplicationKey:              "imports: [config/zmall/shared/base.yaml]\n" + tc.local,
				"config/zmall/shared/base.yaml": tc.imported,
			})
			if err != nil {
				t.Fatal(err)
			}
			port, err := parsePort(application)
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid final port was accepted")
				}
			} else if err != nil || port != tc.want {
				t.Fatalf("port=%d err=%v, want %d", port, err, tc.want)
			}
		})
	}
}

// TestInvalidImportsFailBeforeReadingFragments 验证声明类型、Key、重复及自引用均在读取引用前被拒绝。
func TestInvalidImportsFailBeforeReadingFragments(t *testing.T) {
	for _, declaration := range []string{
		"null", "{}", "config/zmall/shared/base.yaml", "[null]", "[1]", "['']",
		"[../database.yaml]", "[/config/database.yaml]", "[https://example.com/config.yaml]",
		"[config/shared/../database.yaml]", "[config/shared//database.yaml]", "['config/shared/*.yaml']",
		"[config/zmall/shared/base.yaml, config/zmall/shared/base.yaml]",
		"[config/zmall/product/application.yaml]",
	} {
		_, calls, err := loadTestFiles(map[string]string{testApplicationKey: "imports: " + declaration})
		if err == nil || len(calls) != 1 {
			t.Fatalf("invalid imports accepted or fetched fragments: calls=%v err=%v", calls, err)
		}
	}
}

// TestInvalidImportedDocumentsFail 验证引用文件仍须合法且不允许继续声明 imports。
func TestInvalidImportedDocumentsFail(t *testing.T) {
	for _, body := range []string{
		"", "null", "[]", "invalid: [", "{}\n---\n{}",
		"database: {host: first, host: second}", "database: {1: secret-value}",
		"items: [{1: secret-value}]", "1: secret-value",
		"imports: []", "imports: [config/zmall/product/application.yaml]",
	} {
		application, _, err := loadTestFiles(map[string]string{
			testApplicationKey:              "imports: [config/zmall/shared/base.yaml]\nservice: {port: 9088}",
			"config/zmall/shared/base.yaml": body,
		})
		if err == nil || application != nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid fragment accepted or exposed: %v", err)
		}
	}
}

// TestEmptyImportsPreserveSingleDocument 验证空导入列表不增加请求且从最终配置中移除。
func TestEmptyImportsPreserveSingleDocument(t *testing.T) {
	application, calls, err := loadTestFiles(map[string]string{testApplicationKey: "imports: []\nservice: {port: 9088}"})
	if err != nil || len(calls) != 1 || !reflect.DeepEqual(application, map[string]any{"service": map[string]any{"port": 9088}}) {
		t.Fatalf("empty imports changed configuration: calls=%v err=%v", calls, err)
	}
}
