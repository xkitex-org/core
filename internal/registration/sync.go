package registration

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/hashicorp/consul/api"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// loop 合并状态通知并定期续约，网络错误重试时始终重新读取当前实例状态。
func (c *Controller) loop() {
	defer c.workers.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.state.Changes():
		case <-ticker.C:
		}
		c.mu.Lock()
		if c.closed || c.ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		err := c.synchronize()
		if err != nil && c.ctx.Err() == nil {
			if !c.syncFailed {
				klog.Errorf("core: 实例检查同步失败：service=[%s] instance=[%s] error=[%v]", c.settings.ServiceName, c.id, err)
			}
			c.syncFailed = true
		}
		c.mu.Unlock()
	}
}

// update 为单次 TTL 上报设置超时，远端失败不阻塞业务探针及 RPC。
func (c *Controller) update(id, status string) error {
	ctx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	return c.client.Agent().UpdateTTLOpts(id, "xkitex "+status, status, (&api.QueryOptions{}).WithContext(ctx))
}

// synchronize 串行续约与发布就绪结果，登记丢失时先重建 critical 检查再应用最新状态。
func (c *Controller) synchronize() error {
	if c.state.Phase() != string(lifecycle.Running) {
		return nil
	}
	// 定期续约独立心跳，保留 TTL 余量。
	if err := c.update(c.heartbeatID, api.HealthPassing); err != nil {
		if statusCode(err) == http.StatusNotFound {
			if _, err := c.findInstance(true); err != nil {
				return err
			}
			if err := c.bootstrap(); err != nil {
				return err
			}
			c.appliedKnown = false
			klog.Infof("core: 实例检查已重建：service=[%s] instance=[%s]", c.settings.ServiceName, c.id)
		} else {
			return safeError("renew heartbeat", err)
		}
	}
	if !c.state.MonitorEnabled() {
		if c.syncFailed {
			klog.Infof("core: 实例检查同步已恢复：service=[%s] instance=[%s]", c.settings.ServiceName, c.id)
		}
		c.syncFailed = false
		return nil
	}
	ready := c.state.Available()
	status := api.HealthCritical
	if ready {
		status = api.HealthPassing
	}
	if err := c.update(c.readinessID, status); err != nil {
		if statusCode(err) == http.StatusNotFound {
			if _, err := c.findInstance(true); err != nil {
				return err
			}
			if err := c.bootstrap(); err != nil {
				return err
			}
			c.appliedKnown = false
		}
		return safeError("update readiness", err)
	}
	if !c.appliedKnown || c.applied != ready || c.syncFailed {
		klog.Infof("core: 实例就绪检查同步成功：service=[%s] instance=[%s] status=[%s]", c.settings.ServiceName, c.id, status)
	}
	c.appliedKnown, c.applied, c.syncFailed = true, ready, false
	return nil
}
