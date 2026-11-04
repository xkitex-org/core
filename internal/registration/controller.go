// Package registration 对接 registry-consul，并将实例就绪状态同步到独立 TTL 检查。
package registration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/hashicorp/consul/api"
	consul "github.com/kitex-contrib/registry-consul"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

const (
	managedTag     = "xkitex.managed"
	ownerTag       = "xkitex.owner"
	requestTimeout = 3 * time.Second
)

// Controller 管理单个 Server 实例；网络操作串行执行，应用探针不等待远端请求。
type Controller struct {
	mu                                  sync.Mutex
	settings                            config.Settings
	state                               *lifecycle.State
	client                              *api.Client
	delegate                            registry.Registry
	bridge                              *libraryTransport
	info                                *registry.Info
	id, owner, heartbeatID, readinessID string
	ctx                                 context.Context
	cancel                              context.CancelFunc
	workers                             sync.WaitGroup
	attempted, registered, closed       bool
	appliedKnown, applied, syncFailed   bool
	closeErr                            error
	ttl, interval                       time.Duration
}

// New 保存同一启动连接和实例状态，不发请求、不登记服务、不启动协程。
func New(parent context.Context, settings config.Settings, state *lifecycle.State) *Controller {
	ctx, cancel := context.WithCancel(parent)
	return &Controller{settings: settings, state: state, client: settings.ConsulClient(), owner: rand.Text(), ctx: ctx, cancel: cancel, ttl: 15 * time.Second, interval: 5 * time.Second}
}

// Register 由 Kitex 在监听后调用；接探针时首次写入就携带 critical 检查。
func (c *Controller) Register(source *registry.Info) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.registered || c.client == nil || c.ctx.Err() != nil || c.state.Phase() != string(lifecycle.Starting) {
		return fmt.Errorf("core: Consul registration lifecycle is invalid")
	}
	defer func() {
		if err != nil {
			c.state.Finish(err)
			c.cancel()
			c.closed = true
			c.closeErr = c.stopLocked()
			err = errors.Join(err, c.closeErr)
		}
	}()
	c.info, c.id, err = instanceInfo(source, c.settings.ServiceName, c.owner)
	if err != nil {
		return err
	}
	c.heartbeatID = "xkitex:" + c.id + ":heartbeat:" + c.owner
	c.readinessID = "xkitex:" + c.id + ":readiness:" + c.owner
	if err = c.verifyExisting(false); err != nil {
		return err
	}
	libraryClient, err := c.settings.RegistryClient(func(base http.RoundTripper) http.RoundTripper {
		c.bridge = &libraryTransport{base: base, ctx: c.ctx}
		return c.bridge
	})
	if err != nil {
		return safeError("create registry client", err)
	}
	c.delegate, err = consul.NewConsulRegisterWithClient(libraryClient, consul.WithCheck(c.heartbeatCheck()))
	if err != nil {
		return safeError("create registry", err)
	}
	klog.Infof("core: 开始登记实例：service=[%s] instance=[%s] addr=[%s] monitor=[%t]", c.settings.ServiceName, c.id, c.info.Addr, c.state.MonitorEnabled())
	c.attempted = true
	if err = c.bootstrap(); err != nil {
		return err
	}
	if err = c.delegate.Register(c.info); err != nil {
		return safeError("register instance", err)
	}
	if c.ctx.Err() != nil || c.state.Phase() != string(lifecycle.Starting) {
		return fmt.Errorf("core: Consul registration canceled during shutdown")
	}
	c.registered = true
	c.state.Run()
	initial := api.HealthPassing
	if c.state.MonitorEnabled() {
		initial = api.HealthCritical
	}
	klog.Infof("core: 实例登记成功：service=[%s] instance=[%s] addr=[%s] initial_status=[%s]", c.settings.ServiceName, c.id, c.info.Addr, initial)
	c.workers.Add(1)
	go c.loop()
	return nil
}

// heartbeatCheck 为每次启动分配独立心跳 ID，旧进程的迟到心跳不能更新新检查。
func (c *Controller) heartbeatCheck() *api.AgentServiceCheck {
	return &api.AgentServiceCheck{CheckID: c.heartbeatID, Name: "xkitex heartbeat", TTL: c.ttl.String(), Status: api.HealthPassing}
}

// definition 生成当前实例独占的检查集合，readiness 始终以 critical 创建。
func (c *Controller) definition() *api.AgentServiceRegistration {
	address := c.info.Addr.(*net.TCPAddr)
	tags := make([]string, 0, len(c.info.Tags))
	for key, value := range c.info.Tags {
		tags = append(tags, key+":"+value)
	}
	sort.Strings(tags)
	checks := api.AgentServiceChecks{c.heartbeatCheck()}
	if c.state.MonitorEnabled() {
		checks = append(checks, &api.AgentServiceCheck{CheckID: c.readinessID, Name: "xkitex readiness", TTL: c.ttl.String(), Status: api.HealthCritical})
	}
	return &api.AgentServiceRegistration{ID: c.id, Name: c.settings.ServiceName, Address: address.IP.String(), Port: address.Port, Tags: tags, Weights: &api.AgentWeights{Passing: c.info.Weight, Warning: c.info.Weight}, Checks: checks}
}

// bootstrap 原子登记服务和初始检查，并清理同一受管实例上一次启动遗留的检查。
func (c *Controller) bootstrap() error {
	ctx, cancel := context.WithTimeout(c.ctx, requestTimeout)
	defer cancel()
	err := c.client.Agent().ServiceRegisterOpts(c.definition(), (api.ServiceRegisterOpts{ReplaceExistingChecks: true}).WithContext(ctx))
	if err != nil {
		return safeError("initialize instance checks", err)
	}
	return nil
}

// verifyExisting 检查将被操作的实例归属，不删除或覆盖其他应用的服务记录。
func (c *Controller) verifyExisting(requireOwner bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	existing, _, err := c.client.Agent().Service(c.id, (&api.QueryOptions{}).WithContext(ctx))
	if statusCode(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return safeError("read instance ownership", err)
	}
	managed, owner := false, false
	for _, tag := range existing.Tags {
		managed = managed || tag == managedTag+":true"
		owner = owner || tag == ownerTag+":"+c.owner
	}
	if !managed || requireOwner && !owner {
		return fmt.Errorf("core: Consul instance belongs to another owner")
	}
	return nil
}

// Deregister 先禁止检查恢复，再进入维护状态并由 registry-consul 注销，随后停止所有同步任务。
func (c *Controller) Deregister(_ *registry.Info) error {
	c.state.Stop()
	c.cancel()
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.closeErr = c.stopLocked()
	}
	err := c.closeErr
	c.mu.Unlock()
	c.workers.Wait()
	return err
}

// stopLocked 保留真实远端清理结果；无论 Consul 是否可达，都回收第三方的私有心跳。
func (c *Controller) stopLocked() error {
	var result error
	if c.attempted {
		result = c.verifyExisting(true)
		if result == nil {
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			maintenanceErr := c.client.Agent().EnableServiceMaintenanceOpts(c.id, "xkitex shutting down", (&api.QueryOptions{}).WithContext(ctx))
			cancel()
			if maintenanceErr != nil && statusCode(maintenanceErr) != http.StatusNotFound {
				klog.Warnf("core: 实例维护状态同步失败：service=[%s] instance=[%s] error=[%v]", c.settings.ServiceName, c.id, safeError("enable maintenance", maintenanceErr))
			}
			for attempt := 0; attempt < 2; attempt++ {
				if c.delegate != nil {
					result = c.delegate.Deregister(c.info)
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
					result = c.client.Agent().ServiceDeregisterOpts(c.id, (&api.QueryOptions{}).WithContext(ctx))
					cancel()
				}
				if result == nil {
					break
				}
			}
			if result != nil {
				result = safeError("deregister instance", result)
			}
		}
		if result == nil {
			klog.Infof("core: 实例注销成功：service=[%s] instance=[%s]", c.settings.ServiceName, c.id)
		} else {
			klog.Errorf("core: 实例注销未确认：service=[%s] instance=[%s] error=[%v]", c.settings.ServiceName, c.id, result)
		}
	}
	if c.bridge != nil {
		c.bridge.close()
		if c.delegate != nil {
			// 仅让第三方调用取消函数；本地完成不覆盖上面保存的真实注销错误。
			_ = c.delegate.Deregister(c.info)
		}
	}
	c.registered = false
	return result
}

// statusCode 提取 SDK 状态码，不读取或输出错误响应正文。
func statusCode(err error) int {
	var status api.StatusError
	if errors.As(err, &status) {
		return status.Code
	}
	return 0
}

// safeError 只保留操作名称与安全状态，不输出 SDK 原始正文或网络错误。
func safeError(action string, err error) error {
	if code := statusCode(err); code != 0 {
		return fmt.Errorf("core: Consul %s failed: HTTP %d", action, code)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("core: Consul %s: %w", action, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("core: Consul %s: %w", action, context.DeadlineExceeded)
	}
	return fmt.Errorf("core: Consul %s failed", action)
}
