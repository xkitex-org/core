package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadApplication 按 imports 顺序加载单层引用，保留最终值及其覆盖信息。
func loadApplication(ctx context.Context, key string, read func(context.Context, string) ([]byte, error)) (*applicationConfig, error) {
	body, err := read(ctx, key)
	if err != nil {
		return nil, err
	}
	application, err := decodeDocument(body)
	if err != nil {
		return nil, fmt.Errorf("core: configuration %q: %w", key, err)
	}
	imports, err := takeImports(application, key)
	if err != nil {
		return nil, fmt.Errorf("core: configuration %q: %w", key, err)
	}
	merged := &applicationConfig{values: make(map[string]any), sources: make(map[string]*fieldSource)}
	for _, importedKey := range imports {
		body, err := read(ctx, importedKey)
		if err != nil {
			return nil, err
		}
		fragment, err := decodeDocument(body)
		if err != nil {
			return nil, fmt.Errorf("core: configuration %q: %w", importedKey, err)
		}
		if _, nested := fragment["imports"]; nested {
			return nil, fmt.Errorf("core: configuration %q: nested imports are not supported", importedKey)
		}
		merge(merged.values, fragment, merged.sources, importedKey)
	}
	merge(merged.values, application, merged.sources, key)
	return merged, nil
}

// decodeDocument 解析单个 YAML 对象，拒绝多文档及非字符串字段名，并隐藏解析错误中的值。
func decodeDocument(body []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("configuration must contain one YAML object")
	}
	document, ok := value.(map[string]any)
	if !ok || document == nil {
		return nil, fmt.Errorf("configuration must contain one YAML object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain exactly one YAML document")
	}
	if err := validateKeys(document); err != nil {
		return nil, err
	}
	return document, nil
}

// validateKeys 检查所有嵌套对象和列表中的映射，保证字段名均为字符串。
func validateKeys(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for _, child := range value {
			if err := validateKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validateKeys(child); err != nil {
				return err
			}
		}
	case map[any]any:
		return fmt.Errorf("configuration field names must be strings")
	}
	return nil
}

// takeImports 提取并移除入口的导入声明，提前拒绝重复、自引用及非法 Key。
func takeImports(application map[string]any, entryKey string) ([]string, error) {
	value, exists := application["imports"]
	if !exists {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("imports must be a YAML list of Consul keys")
	}
	keys := make([]string, 0, len(items))
	seen := map[string]bool{entryKey: true}
	for i, item := range items {
		key, ok := item.(string)
		key = strings.TrimSpace(key)
		if !ok || !validImportKey(key) {
			return nil, fmt.Errorf("imports item %d must be an explicit key under config/", i+1)
		}
		if seen[key] {
			return nil, fmt.Errorf("imports item %d duplicates an entry or import key", i+1)
		}
		seen[key] = true
		keys = append(keys, key)
	}
	delete(application, "imports")
	return keys, nil
}

// validImportKey 仅接受 config/ 下的明确 Key，拒绝相对路径、URL 和通配符。
func validImportKey(key string) bool {
	if !strings.HasPrefix(key, "config/") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if !validSegment.MatchString(segment) {
			return false
		}
	}
	return true
}
