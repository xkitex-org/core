package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/generic"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	"github.com/hashicorp/consul/api"
)

// registrationMonitor 通过通道交出检查报告入口，并确认 RPC 注销先于探针关闭。
type registrationMonitor struct {
	report         chan func(bool, bool)
	phase          func() string
	isDeregistered func() bool
	closeOrder     bool
}

// ConfigureMonitor 接收隔离配置，不启动其他资源。
func (p *registrationMonitor) ConfigureMonitor([]byte) error { return nil }

// Start 暴露当前实例报告函数，用于验证业务就绪与注册同步之间的真实接线。
func (p *registrationMonitor) Start(_ context.Context, phase func() string, report func(bool, bool)) (<-chan error, error) {
	p.phase = phase
	p.report <- report
	return nil, nil
}

// Close 验证远端注销已经完成。
func (p *registrationMonitor) Close() error { p.closeOrder = p.isDeregistered(); return nil }

// TestManagedServerRegistrationAndMultipleRPC 验证默认启动流程自动登记一个实例，且两个 RPC Service 共用就绪门控。
func TestManagedServerRegistrationAndMultipleRPC(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var mu sync.Mutex
	var instance *api.AgentServiceRegistration
	ready := false
	registrations := 0
	deregistered := false
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Consul-Token") != "test-token" {
			w.WriteHeader(403)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/kv/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(fmt.Sprintf("service: {port: %d}", port))}})
		case r.URL.Path == "/v1/agent/service/register":
			var definition api.AgentServiceRegistration
			if json.NewDecoder(r.Body).Decode(&definition) != nil {
				w.WriteHeader(400)
				return
			}
			instance = &definition
			registrations++
			if registrations == 1 {
				if len(definition.Checks) != 2 || definition.Checks[1].Status != api.HealthCritical {
					t.Error("missing initial critical readiness")
				}
			}
			w.WriteHeader(200)
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/maintenance/"):
			ready = false
			w.WriteHeader(200)
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/deregister/"):
			instance = nil
			deregistered = true
			w.WriteHeader(200)
		case strings.HasPrefix(r.URL.Path, "/v1/agent/service/"):
			if instance == nil {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(api.AgentService{ID: instance.ID, Service: instance.Name, Tags: instance.Tags})
		case strings.HasPrefix(r.URL.Path, "/v1/agent/check/update/"):
			var update struct{ Status string }
			_ = json.NewDecoder(r.Body).Decode(&update)
			if strings.Contains(r.URL.Path, ":readiness:") {
				ready = update.Status == api.HealthPassing
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer agent.Close()
	p := &registrationMonitor{report: make(chan func(bool, bool), 1), isDeregistered: func() bool { mu.Lock(); defer mu.Unlock(); return deregistered }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var services []Registration
	clients := make(map[string]genericclient.Client)
	for _, name := range []string{"ProductService", "CategoryService"} {
		codec := testCodec(t, name)
		handler := &generic.ServiceV2{GenericCall: func(_ context.Context, service, _ string, _ interface{}) (interface{}, error) {
			return `"` + service + `"`, nil
		}}
		services = append(services, NewRegistration(func(s server.Server, h *generic.ServiceV2, options ...server.RegisterOption) error {
			return s.RegisterService(generic.ServiceInfoWithGeneric(codec), h, options...)
		}, handler))
		cli, err := genericclient.NewClient(name, codec, client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", port)), client.WithTransportProtocol(transport.TTHeader), client.WithMetaHandler(transmeta.ClientTTHeaderHandler), client.WithRPCTimeout(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		clients[name] = cli
		defer cli.Close()
	}
	go func() {
		done <- startWithComponents(ctx, configEnvironment(agent.URL), managedServer, nil, NewMonitor(p), services...)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("managed server exit: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("managed server did not stop")
		}
		if !p.closeOrder {
			t.Error("monitor closed before instance deregistration")
		}
	}()
	var report func(bool, bool)
	select {
	case report = <-p.report:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor was not started")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		registered := registrations == 1 && p.phase() == "Running"
		isReady := ready
		mu.Unlock()
		if registered {
			if isReady {
				t.Fatal("instance became ready before business checks")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("default managed server did not register")
		}
		time.Sleep(time.Millisecond)
	}
	report(true, true)
	for {
		mu.Lock()
		isReady := ready
		mu.Unlock()
		if isReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness report did not reach Consul")
		}
		time.Sleep(time.Millisecond)
	}
	for name, cli := range clients {
		response, err := cli.GenericCall(context.Background(), "Get", `{"key":"42"}`)
		if err != nil || strings.Trim(fmt.Sprint(response), `"`) != name {
			t.Fatalf("%s RPC failed: %v", name, err)
		}
	}
	mu.Lock()
	if instance == nil || instance.Name != "demo-api" || instance.Port != port {
		t.Error("wrong application-level RPC registration")
	}
	mu.Unlock()
}
