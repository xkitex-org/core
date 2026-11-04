package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/hashicorp/consul/api"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// agentEvent 保留每次写入后的可发现性，检测初始注册是否出现短暂放行。
type agentEvent struct {
	operation, id string
	available     bool
	definition    *api.AgentServiceRegistration
}

// fakeAgent 模拟 Agent 检查保留、TTL 更新和维护模式，不访问任何真实环境。
type fakeAgent struct {
	mu                        sync.Mutex
	service                   *api.AgentServiceRegistration
	checks                    map[string]*api.AgentServiceCheck
	maintenance               bool
	events                    []agentEvent
	requests                  int
	badAuth                   bool
	failDeregister            int
	failAll, failRegistration int
	registrationCalls         int
	commitFailedRegistration  bool
	server                    *httptest.Server
}

// newAgent 启动带固定测试 ACL 校验的临时 HTTP Agent。
func newAgent(t *testing.T) *fakeAgent {
	t.Helper()
	a := &fakeAgent{checks: make(map[string]*api.AgentServiceCheck)}
	a.server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.server.Close)
	return a
}

// available 在持锁期间计算当前实例是否能被仅健康查询发现。
func (a *fakeAgent) available() bool {
	if a.service == nil || a.maintenance {
		return false
	}
	for _, check := range a.checks {
		if check.Status != api.HealthPassing {
			return false
		}
	}
	return true
}

// snapshot 返回受保护的实例可用性。
func (a *fakeAgent) snapshot() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.available() }

// serve 实现本轮注册使用的最小 Agent API，并按真实语义保留未替换的检查。
func (a *fakeAgent) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	if r.Header.Get("X-Consul-Token") != "registry-test-token" {
		a.badAuth = true
		http.Error(w, "private-response-token", 403)
		return
	}
	path := r.URL.Path
	if a.failDeregister != 0 && strings.HasPrefix(path, "/v1/agent/service/deregister/") {
		http.Error(w, "private-response-token", a.failDeregister)
		return
	}
	if a.failAll != 0 {
		http.Error(w, "private-response-token", a.failAll)
		return
	}
	switch {
	case strings.HasPrefix(path, "/v1/kv/"):
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("{}")}})
	case path == "/v1/agent/service/register":
		var def api.AgentServiceRegistration
		if json.NewDecoder(r.Body).Decode(&def) != nil {
			w.WriteHeader(400)
			return
		}
		a.registrationCalls++
		failing := a.failRegistration == a.registrationCalls
		if !failing || a.commitFailedRegistration {
			if r.URL.Query().Get("replace-existing-checks") == "true" {
				a.checks = make(map[string]*api.AgentServiceCheck)
				a.maintenance = false
			}
			checks := append(api.AgentServiceChecks{}, def.Checks...)
			if def.Check != nil {
				checks = append(checks, def.Check)
			}
			for _, check := range checks {
				copy := *check
				if copy.Status == "" {
					copy.Status = api.HealthCritical
				}
				a.checks[copy.CheckID] = &copy
			}
			a.service = &def
			a.events = append(a.events, agentEvent{operation: "register", id: def.ID, available: a.available(), definition: &def})
		}
		if failing {
			http.Error(w, "private-response-token", 500)
			return
		}
		w.WriteHeader(200)
	case strings.HasPrefix(path, "/v1/agent/service/maintenance/"):
		if a.service == nil {
			w.WriteHeader(404)
			return
		}
		a.maintenance = r.URL.Query().Get("enable") == "true"
		a.events = append(a.events, agentEvent{operation: "maintenance", available: a.available()})
		w.WriteHeader(200)
	case strings.HasPrefix(path, "/v1/agent/service/deregister/"):
		a.service = nil
		a.checks = make(map[string]*api.AgentServiceCheck)
		a.events = append(a.events, agentEvent{operation: "deregister", available: false})
		w.WriteHeader(200)
	case strings.HasPrefix(path, "/v1/agent/service/"):
		if a.service == nil {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(api.AgentService{ID: a.service.ID, Service: a.service.Name, Tags: a.service.Tags, Address: a.service.Address, Port: a.service.Port})
	case strings.HasPrefix(path, "/v1/agent/check/update/"):
		id := strings.TrimPrefix(path, "/v1/agent/check/update/")
		check := a.checks[id]
		if check == nil {
			w.WriteHeader(404)
			return
		}
		var body struct{ Status string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		check.Status = body.Status
		a.events = append(a.events, agentEvent{operation: "update", id: id, available: a.available()})
		w.WriteHeader(200)
	default:
		w.WriteHeader(404)
	}
}

// fixture 通过真实配置加载建立注册实例，确保 SDK 和第三方使用相同端点及 ACL。
func fixture(t *testing.T, a *fakeAgent, gated bool) (*Controller, *registry.Info) {
	t.Helper()
	values := map[string]string{"X_SERVICE_GROUP": "zmall", "X_SERVICE_NAME": "product", "X_CONSUL_ADDR": a.server.URL, "X_CONSUL_TOKEN": "registry-test-token"}
	settings, err := config.Load(context.Background(), func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(settings.CloseConsul)
	state := lifecycle.New(settings.ServiceName, gated)
	c := New(context.Background(), settings, state)
	c.interval = 5 * time.Millisecond
	c.ttl = 2 * time.Second
	t.Cleanup(func() { _ = c.Deregister(nil) })
	return c, &registry.Info{ServiceName: settings.ServiceName, Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9888}, Weight: 10}
}

// eventually 等待可观察的 Agent 结果，避免测试依赖某个后台任务调度时刻。
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("Consul state did not converge")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestDirectRegistrationAndShutdown 验证无探针首次就 passing、单一实例登记及维护先于注销。
func TestDirectRegistrationAndShutdown(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, false)
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	if !c.state.Available() || !a.snapshot() {
		t.Fatal("ungated instance waited for readiness")
	}
	a.mu.Lock()
	validFirst := len(a.events) > 0 && a.events[0].available && len(a.events[0].definition.Checks) == 1 && a.events[0].definition.Port == 9888
	a.mu.Unlock()
	if !validFirst {
		t.Fatal("first registration was not available on the RPC port")
	}

	if err := c.Deregister(info); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.badAuth || a.service != nil || a.events[len(a.events)-2].operation != "maintenance" || a.events[len(a.events)-1].operation != "deregister" {
		t.Fatal("wrong ACL, maintenance or deregistration ordering")
	}
}

// TestReadinessGateRecoveryAndLateResults 验证首次登记无放行窗口、readiness 失败恢复和停机不可逆。
func TestReadinessGateRecoveryAndLateResults(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, true)
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	safe := len(a.checks) == 2
	for _, event := range a.events {
		safe = safe && !event.available
	}
	a.mu.Unlock()
	if !safe {
		t.Fatal("initial registration bypassed independent readiness gate")
	}

	c.state.Report(true, true)
	eventually(t, a.snapshot)
	c.state.Report(false, false)
	eventually(t, func() bool { return !a.snapshot() })
	c.state.Report(false, true)
	eventually(t, a.snapshot)
	if err := c.Deregister(info); err != nil {
		t.Fatal(err)
	}
	c.state.Report(true, true)
	c.state.Run()
	if c.state.Available() || a.snapshot() {
		t.Fatal("late result revived stopped instance")
	}
}

// TestStaleMonitorChecksAreReplaced 验证上一轮带探针的检查不会影响本轮不带探针实例。
func TestStaleMonitorChecksAreReplaced(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, false)
	a.service = &api.AgentServiceRegistration{ID: "zmall-product:127.0.0.1:9888", Name: "zmall-product", Tags: []string{managedTag + ":true", ownerTag + ":old"}}
	a.checks["old-readiness"] = &api.AgentServiceCheck{CheckID: "old-readiness", TTL: "15s", Status: api.HealthCritical}
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.available() || a.checks["old-readiness"] != nil || len(a.checks) != 1 {
		t.Fatal("old monitor checks prevented direct registration")
	}
}

// TestRegistrationFailureRollsBackAndRedacts 验证服务端已提交但返回错误时仍清理实例，并隐藏响应正文。
func TestRegistrationFailureRollsBackAndRedacts(t *testing.T) {
	for _, step := range []int{1, 2} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			a := newAgent(t)
			c, info := fixture(t, a, true)
			a.failRegistration = step
			a.commitFailedRegistration = true
			err := c.Register(info)
			if err == nil || strings.Contains(err.Error(), "private-response-token") || c.state.Phase() != "Failed" {
				t.Fatalf("unsafe registration result: %v", err)
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.service != nil {
				t.Fatal("partial registration was retained")
			}
		})
	}
}

// TestForeignInstanceIsNotOverwritten 验证实例 ID 冲突时不覆盖或删除非 core 管理的实例。
func TestForeignInstanceIsNotOverwritten(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, true)
	a.service = &api.AgentServiceRegistration{ID: "zmall-product:127.0.0.1:9888", Name: "zmall-product"}
	if err := c.Register(info); err == nil {
		t.Fatal("unmanaged instance overwritten")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.service == nil || len(a.events) != 0 {
		t.Fatal("foreign instance modified")
	}
}

// TestAgentRestartRebuildsCriticalGate 验证 Agent 丢失登记后先重建不可用检查，再恢复当前就绪状态。
func TestAgentRestartRebuildsCriticalGate(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, true)
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	c.state.Report(true, true)
	eventually(t, a.snapshot)
	a.mu.Lock()
	a.service = nil
	a.checks = make(map[string]*api.AgentServiceCheck)
	a.events = nil
	a.mu.Unlock()
	eventually(t, a.snapshot)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 || a.events[0].operation != "register" || a.events[0].available {
		t.Fatal("restoration bypassed initial critical state")
	}
}

// logOutput 支持第三方心跳和测试同时访问日志缓冲。
type logOutput struct {
	mu   sync.Mutex
	body bytes.Buffer
}

// Write 记录并发 klog 输出。
func (o *logOutput) Write(body []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.body.Write(body)
}

// String 返回受保护的日志快照。
func (o *logOutput) String() string { o.mu.Lock(); defer o.mu.Unlock(); return o.body.String() }

// TestUnavailableAgentStillStopsLocalHeartbeat 验证注销无法确认时保留错误，并停止第三方心跳的外部请求。
func TestUnavailableAgentStillStopsLocalHeartbeat(t *testing.T) {
	var output logOutput
	klog.SetOutput(&output)
	defer klog.SetOutput(os.Stderr)
	a := newAgent(t)
	c, info := fixture(t, a, true)
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.failDeregister = 503
	a.mu.Unlock()
	err := c.Deregister(info)
	if err == nil || strings.Contains(err.Error(), "private-response-token") {
		t.Fatalf("remote failure falsely reported success: %v", err)
	}
	a.mu.Lock()
	before := a.requests
	a.mu.Unlock()
	time.Sleep(1100 * time.Millisecond)
	a.mu.Lock()
	after := a.requests
	safelyUnavailable := a.service != nil && a.maintenance
	a.mu.Unlock()
	if !safelyUnavailable || before != after || strings.Contains(output.String(), "private-response-token") || strings.Contains(output.String(), "实例注销成功") {
		t.Fatal("heartbeat leaked or remote cleanup falsely reported success")
	}
}

// TestRuntimeSyncFailureRecovers 验证运行中 Consul 不可达时不退出 RPC，恢复后发布最新就绪结果。
func TestRuntimeSyncFailureRecovers(t *testing.T) {
	a := newAgent(t)
	c, info := fixture(t, a, true)
	if err := c.Register(info); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.failAll = 503
	a.mu.Unlock()
	c.state.Report(true, true)
	eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.syncFailed })
	c.state.Report(false, false)
	a.mu.Lock()
	a.failAll = 0
	a.mu.Unlock()
	eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.syncFailed })
	if a.snapshot() {
		t.Fatal("old passing result replayed after network recovery")
	}
	c.state.Report(false, true)
	eventually(t, a.snapshot)
}
