package config

import (
	"fmt"
	"regexp"
	"strings"
)

var validSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type identity struct {
	name string
	key  string
}

// ServiceName 返回经过校验的应用服务名，供配置读取前的实例日志使用。
func ServiceName(lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("core: environment reader is nil")
	}
	id, err := loadIdentity(lookup)
	return id.name, err
}

// loadIdentity 校验服务分组和名称，并生成应用服务名及固定配置 Key。
func loadIdentity(lookup func(string) (string, bool)) (identity, error) {
	values := make([]string, 2)
	for i, key := range []string{"X_SERVICE_GROUP", "X_SERVICE_NAME"} {
		raw, _ := lookup(key)
		value := strings.TrimSpace(raw)
		if value == "" {
			return identity{}, fmt.Errorf("core: %s is required", key)
		}
		if !validSegment.MatchString(value) {
			return identity{}, fmt.Errorf("core: %s must start with a letter or digit and contain only letters, digits, '.', '_' or '-'", key)
		}
		values[i] = value
	}
	return identity{
		name: values[0] + "-" + values[1],
		key:  "config/" + values[0] + "/" + values[1] + "/application.yaml",
	}, nil
}
