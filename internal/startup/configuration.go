package startup

import (
	"errors"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"regexp"
)

// Configuration 保存显式业务配置声明，构造时不执行回调。
type Configuration struct {
	name      string
	configure func([]byte) error
	err       error
}

var configurationName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// NewConfiguration 拒绝无效名称、保留节和空回调，不允许覆盖框架配置接收器。
func NewConfiguration(name string, configure func([]byte) error) *Configuration {
	c := &Configuration{name: name, configure: configure}
	reserved := map[string]bool{"imports": true, "service": true, "logging": true, "monitor": true, "database": true, "cache": true, "sentinel": true}
	if !configurationName.MatchString(name) || reserved[name] || configure == nil {
		c.err = errors.New("core: 业务配置名称或回调无效")
	}
	return c
}

// configurationComponents 校验重复声明，并隐藏可能包含业务凭据的原始回调错误。
func configurationComponents(items []*Configuration) ([]config.Component, error) {
	seen := make(map[string]bool)
	result := make([]config.Component, 0, len(items))
	for _, item := range items {
		if item == nil || item.err != nil {
			return nil, errors.New("core: 业务配置名称或回调无效")
		}
		if seen[item.name] {
			return nil, errors.New("core: 同一业务配置节只能接入一次")
		}
		seen[item.name] = true
		result = append(result, config.Component{Name: item.name, Configure: func(body []byte) error {
			if err := item.configure(body); err != nil {
				return errors.New("core: 业务配置校验失败")
			}
			return nil
		}})
	}
	return result, nil
}
