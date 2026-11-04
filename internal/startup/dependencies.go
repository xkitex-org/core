package startup

import (
	"context"
	"fmt"
	"sort"
)

// dependency 只描述依赖资源启动与健康检查，不依赖具体数据库或缓存驱动。
type dependency interface {
	Start(context.Context) error
	ReadinessChecks() map[string]func(context.Context) error
}

// startDependency 创建资源句柄，并将逐源就绪检查交给可选探针，不执行额外 Ping。
func startDependency(ctx context.Context, name string, resource dependency, monitoring *Monitoring) error {
	if err := resource.Start(ctx); err != nil {
		return fmt.Errorf("core: start %s: %w", name, err)
	}
	if monitoring == nil {
		return nil
	}
	target, ok := monitoring.controller.(interface {
		AddReadiness(string, func(context.Context) error) error
	})
	if !ok {
		return fmt.Errorf("core: 接入 %s 时 monitor 必须支持 AddReadiness", name)
	}
	checks := resource.ReadinessChecks()
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, checkName := range names {
		if err := target.AddReadiness(checkName, checks[checkName]); err != nil {
			return fmt.Errorf("core: register %s readiness: %w", name, err)
		}
	}
	return nil
}
