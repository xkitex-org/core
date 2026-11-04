package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/generic"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"go.chaoxizhongzhi.com/xkitex/sentinel"
)

// rpcMonitorRecorder 只记录真实 RPC 的中间件调用；独立 monitor 模块另验证具体指标值。
type rpcMonitorRecorder struct {
	MonitorController
	requests atomic.Int32
	failures atomic.Int32
	services sync.Map
}

// Middleware 记录下游结果，用于验证生产选项组装顺序和多 Service 的身份透传。
func (m *rpcMonitorRecorder) Middleware() endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request, response interface{}) error {
			m.requests.Add(1)
			if info := rpcinfo.GetRPCInfo(ctx); info != nil && info.Invocation() != nil {
				m.services.Store(info.Invocation().ServiceName(), true)
			}
			err := next(ctx, request, response)
			if err != nil {
				m.failures.Add(1)
			}
			return err
		}
	}
}

// TestSentinelDynamicRulesProtectRealRPC 验证真实 Kitex 请求随 Consul 规则热更新阻断和恢复，两个 IDL Service 不串资源。
func TestSentinelDynamicRulesProtectRealRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	var blocked atomic.Bool
	var version atomic.Uint64
	version.Store(1)
	var configurationReads atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Consul-Token") != "test-token" {
			w.WriteHeader(403)
			return
		}
		if r.URL.Path == "/v1/kv/config/demo/api/application.yaml" {
			configurationReads.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(fmt.Sprintf("service: {port: %d}\n%s", port, sentinelApplication))}})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/kv/config/demo/api/sentinel/") {
			w.WriteHeader(404)
			return
		}
		if r.URL.Query().Get("index") != "" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		w.Header().Set("X-Consul-Index", fmt.Sprint(version.Load()))
		body := []byte("[]")
		if strings.HasSuffix(r.URL.Path, "/flow") && blocked.Load() {
			body = []byte(`[{"resource":"demo-api/ProductService/Get","threshold":0,"statIntervalInMs":1000}]`)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": body}})
	}))
	defer consul.Close()
	controller := sentinel.New()
	monitor := &rpcMonitorRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var registrations []Registration
	clients := map[string]genericclient.Client{}
	for _, name := range []string{"ProductService", "CategoryService"} {
		codec := testCodec(t, name)
		handler := &generic.ServiceV2{GenericCall: func(context.Context, string, string, any) (any, error) { return `"ok"`, nil }}
		registrations = append(registrations, NewRegistration(func(s server.Server, h *generic.ServiceV2, opts ...server.RegisterOption) error {
			return s.RegisterService(generic.ServiceInfoWithGeneric(codec), h, opts...)
		}, handler))
		cli, err := genericclient.NewClient(name, codec, client.WithHostPorts(listener.Addr().String()), client.WithTransportProtocol(transport.TTHeader), client.WithMetaHandler(transmeta.ClientTTHeaderHandler), client.WithRPCTimeout(200*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		clients[name] = cli
		defer cli.Close()
	}
	go func() {
		done <- startWithSentinel(ctx, configEnvironment(consul.URL), func(_ context.Context, settings config.Settings, _ *lifecycle.State, exit <-chan error) server.Server {
			options := middlewareOptions(NewMonitor(monitor), NewSentinel(controller))
			options = append(options, server.WithListener(listener), server.WithExitSignal(func() <-chan error { return exit }), server.WithExitWaitTime(10*time.Millisecond))
			return newServer(settings, options...)
		}, nil, nil, nil, nil, NewSentinel(controller), registrations...)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("guarded RPC did not stop")
		}
	}()
	waitRPC := func(condition func(error) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := clients["ProductService"].GenericCall(context.Background(), "Get", `{"key":"1"}`)
			if condition(err) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("RPC state did not change: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitRPC(func(err error) bool { return err == nil })
	blocked.Store(true)
	version.Add(1)
	waitRPC(func(err error) bool { return err != nil && strings.Contains(strings.ToLower(err.Error()), "block") })
	if _, err := clients["CategoryService"].GenericCall(context.Background(), "Get", `{"key":"1"}`); err != nil {
		t.Fatalf("another service was blocked: %v", err)
	}
	blocked.Store(false)
	version.Add(1)
	waitRPC(func(err error) bool { return err == nil })
	if monitor.requests.Load() < 3 || monitor.failures.Load() == 0 {
		t.Fatal("monitor did not observe successful and Sentinel-blocked calls")
	}
	for _, service := range []string{"ProductService", "CategoryService"} {
		if _, ok := monitor.services.Load(service); !ok {
			t.Fatal("monitor lost IDL Service identity")
		}
	}

	if configurationReads.Load() != 1 {
		t.Fatal("application.yaml reloaded while rules changed")
	}
}
