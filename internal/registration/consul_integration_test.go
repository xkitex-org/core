package registration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/hashicorp/consul/api"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// unusedPort 为隔离 Agent 选择本机端口，不使用生产或测试 Consul 的默认端口。
func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// realWait 等待真实 Agent 与 Catalog 的异步收敛，保持有限的验证时间。
func realWait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("isolated Consul did not converge")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// devAgent 以临时目录和随机端口启动 ACL 默认拒绝的真实 Consul，不打印管理 Token。
func devAgent(t *testing.T) (*api.Client, string, string) {
	t.Helper()
	binary, err := exec.LookPath("consul")
	if err != nil {
		t.Skip("本机未安装 consul，跳过真实 Agent 验证")
	}
	root := t.TempDir()
	var secret [16]byte
	_, err = rand.Read(secret[:])
	if err != nil {
		t.Fatal("cannot create isolated ACL secret")
	}
	token := fmt.Sprintf("%x-%x-%x-%x-%x", secret[:4], secret[4:6], secret[6:8], secret[8:10], secret[10:])
	port := unusedPort(t)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	configuration := map[string]any{
		"datacenter": "xkitex-isolated", "node_name": "xkitex-isolated", "bind_addr": "127.0.0.1", "client_addr": "127.0.0.1", "advertise_addr": "127.0.0.1",
		"ports": map[string]int{"http": port, "https": -1, "dns": -1, "server": unusedPort(t), "serf_lan": unusedPort(t), "serf_wan": unusedPort(t), "grpc": -1, "grpc_tls": -1},
		"acl":   map[string]any{"enabled": true, "default_policy": "deny", "enable_token_persistence": false, "tokens": map[string]string{"initial_management": token, "agent": token}},
	}
	body, _ := json.Marshal(configuration)
	path := filepath.Join(root, "agent.json")
	if os.WriteFile(path, body, 0o600) != nil {
		t.Fatal("cannot create isolated Consul config")
	}
	output, err := os.OpenFile(filepath.Join(root, "agent.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("cannot create isolated Consul log")
	}
	command := exec.Command(binary, "agent", "-dev", "-config-file", path, "-data-dir", filepath.Join(root, "data"), "-log-level=warn")
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		_ = output.Close()
		t.Fatal("cannot start isolated Consul")
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait(); _ = output.Close() })
	client, err := api.NewClient(&api.Config{Address: address, Scheme: "http", Token: token, HttpClient: &http.Client{Timeout: time.Second}})
	if err != nil {
		t.Fatal("cannot create isolated management client")
	}
	var policy *api.ACLPolicy
	realWait(t, func() bool {
		var err error
		policy, _, err = client.ACL().PolicyCreate(&api.ACLPolicy{Name: "xkitex-app", Rules: `service_prefix "zmall-" { policy = "write" }
key_prefix "config/zmall/" { policy = "read" }`}, nil)
		return err == nil
	})
	app, _, err := client.ACL().TokenCreate(&api.ACLToken{Description: "isolated xkitex app", Policies: []*api.ACLTokenPolicyLink{{ID: policy.ID}}}, nil)
	if err != nil {
		t.Fatal("cannot create isolated application ACL")
	}
	if _, err := client.KV().Put(&api.KVPair{Key: "config/zmall/product/application.yaml", Value: []byte("{}")}, nil); err != nil {
		t.Fatal("cannot seed isolated application config")
	}
	return client, address, app.SecretID
}

// TestConsulACLRegistrationAndTTL 验证真实 ACL、检查原子创建、健康查询、Agent 登记恢复和 TTL 失效。
func TestConsulACLRegistrationAndTTL(t *testing.T) {
	management, address, token := devAgent(t)
	for _, gated := range []bool{false, true} {
		t.Run(fmt.Sprintf("monitor=%t", gated), func(t *testing.T) {
			values := map[string]string{"X_SERVICE_GROUP": "zmall", "X_SERVICE_NAME": "product", "X_CONSUL_ADDR": address, "X_CONSUL_TOKEN": token}
			settings, err := config.Load(context.Background(), func(key string) (string, bool) { value, ok := values[key]; return value, ok })
			if err != nil {
				t.Fatal("application ACL could not read isolated config")
			}
			defer settings.CloseConsul()
			state := lifecycle.New(settings.ServiceName, gated)
			controller := New(context.Background(), settings, state)
			controller.ttl = 2 * time.Second
			controller.interval = 50 * time.Millisecond
			info := &registry.Info{ServiceName: settings.ServiceName, Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: unusedPort(t)}, Weight: 10}
			if err := controller.Register(info); err != nil {
				t.Fatalf("isolated registration failed: %v", err)
			}
			defer controller.Deregister(info)
			healthy := func() int {
				records, _, err := management.Health().Service(settings.ServiceName, "", true, nil)
				if err != nil {
					return -1
				}
				count := 0
				for _, record := range records {
					if record.Service.ID == controller.id {
						count++
					}
				}
				return count
			}
			if gated {
				checks, err := management.Agent().Checks()
				if err != nil || checks[controller.readinessID] == nil || checks[controller.heartbeatID] == nil || checks[controller.readinessID].Status != api.HealthCritical {
					t.Fatal("real Agent did not preserve critical readiness beside heartbeat")
				}
				if healthy() != 0 {
					t.Fatal("unready instance was discoverable")
				}
				state.Report(true, true)
				realWait(t, func() bool { return healthy() == 1 })
				state.Report(false, false)
				realWait(t, func() bool { return healthy() == 0 })
				state.Report(false, true)
			}
			realWait(t, func() bool { return healthy() == 1 })
			if err := management.Agent().ServiceDeregister(controller.id); err != nil {
				t.Fatal("cannot simulate lost Agent registration")
			}
			realWait(t, func() bool { return healthy() == 1 })
			// 等待本地 Agent 实际恢复，不能把 Catalog 的旧缓存误判为已重建。
			realWait(t, func() bool { _, _, err := management.Agent().Service(controller.id, nil); return err == nil })
			realWait(t, func() bool { return healthy() == 1 })
			// 停止所有应用续约，保留 Agent 中的登记，模拟进程无法继续更新检查。
			controller.cancel()
			controller.workers.Wait()
			realWait(t, func() bool { return healthy() == 0 })
			if _, _, err := management.Agent().Service(controller.id, nil); err != nil {
				t.Fatal("lease expiration unexpectedly deleted recoverable service")
			}
			if err := controller.Deregister(info); err != nil {
				t.Fatalf("isolated deregistration failed: %v", err)
			}
			if _, _, err := management.Agent().Service(controller.id, nil); statusCode(err) != 404 {
				t.Fatal("isolated instance was not deregistered")
			}
		})
	}
}
