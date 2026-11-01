package config

import (
	"sort"

	"github.com/cloudwego/kitex/pkg/klog"
)

// applicationConfig 保存最终配置及来源信息，仅用于启动期间的解析和覆盖日志。
type applicationConfig struct {
	values  map[string]any
	sources map[string]*fieldSource
}

// fieldSource 记录当前字段的生效文件及是否覆盖过已有配置，不保留历史文件或配置值。
type fieldSource struct {
	file       string
	overridden bool
	children   map[string]*fieldSource
}

// merge 以 source 为高优先级递归合并，同时更新最终字段来源及覆盖标记。
func merge(target, source map[string]any, sources map[string]*fieldSource, file string) {
	for key, incoming := range source {
		existing, exists := target[key]
		existingMap, existingIsMap := existing.(map[string]any)
		incomingMap, incomingIsMap := incoming.(map[string]any)
		if existingIsMap && incomingIsMap {
			origin := sources[key]
			merge(existingMap, incomingMap, origin.children, file)
		} else {
			target[key] = incoming
			sources[key] = sourceFor(incoming, file, exists)
		}
	}
}

// sourceFor 为当前值建立来源记录；对象整体替换时，将覆盖标记传给其最终字段。
func sourceFor(value any, file string, overridden bool) *fieldSource {
	source := &fieldSource{file: file, overridden: overridden}
	if fields, ok := value.(map[string]any); ok {
		source.children = make(map[string]*fieldSource, len(fields))
		for key, child := range fields {
			source.children[key] = sourceFor(child, file, overridden)
		}
	}
	return source
}

// collectOverrides 收集最终仍存在的覆盖字段，列表及空对象以自身字段名记录。
func collectOverrides(sources map[string]*fieldSource, prefix string, overrides map[string]string) {
	for key, source := range sources {
		field := prefix + key
		if len(source.children) > 0 {
			collectOverrides(source.children, field+".", overrides)
		} else if source.overridden {
			overrides[field] = source.file
		}
	}
}

// logOverrides 使用 Kitex klog 按字段名排序输出，每个覆盖字段只记录最终生效文件。
func (a *applicationConfig) logOverrides() {
	overrides := make(map[string]string)
	collectOverrides(a.sources, "", overrides)
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		klog.Infof("配置[%s]已被[%s]覆盖。", key, overrides[key])
	}
}
