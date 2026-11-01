package startup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/generic"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

// testCodec 使用小型 IDL 创建测试编解码器，并在测试结束后释放资源。
func testCodec(t *testing.T, name string) generic.Generic {
	t.Helper()
	provider, err := generic.NewThriftContentProvider(fmt.Sprintf("service %s { string Get(1: string key) }", name), nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := generic.JSONThriftGeneric(provider)
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// TestTwoServicesWithSameMethodShareOneListener 验证同一监听端口能按 Service 名区分同名 RPC 方法。
func TestTwoServicesWithSameMethodShareOneListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().String()
	exit := make(chan error, 1)
	done := make(chan error, 1)
	var registrations []Registration
	clients := make(map[string]genericclient.Client)
	for _, name := range []string{"ProductService", "InventoryService"} {
		codec := testCodec(t, name)
		handler := &generic.ServiceV2{GenericCall: func(ctx context.Context, service, method string, _ any) (any, error) {
			if rpcinfo.GetRPCInfo(ctx).To().ServiceName() != "demo-api" {
				return nil, fmt.Errorf("configured application name was not applied")
			}
			if service != name || method != "Get" {
				return nil, fmt.Errorf("wrong dispatch: %s/%s", service, method)
			}
			return `"` + name + `"`, nil
		}}
		register := func(s server.Server, handler *generic.ServiceV2, options ...server.RegisterOption) error {
			return s.RegisterService(generic.ServiceInfoWithGeneric(codec), handler, options...)
		}
		registrations = append(registrations, NewRegistration(register, handler))
		cli, err := genericclient.NewClient(name, codec,
			client.WithHostPorts(address), client.WithTransportProtocol(transport.TTHeader),
			client.WithMetaHandler(transmeta.ClientTTHeaderHandler), client.WithRPCTimeout(200*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		clients[name] = cli
		defer cli.Close()
	}
	port := listener.Addr().(*net.TCPAddr).Port
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kv/config/demo/api/application.yaml" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(fmt.Sprintf("service: {port: %d}", port))}})
	}))
	defer consul.Close()
	go func() {
		done <- start(context.Background(), configEnvironment(consul.URL), func(settings config.Settings) server.Server {
			if settings.Port != port {
				t.Errorf("configured port lost: got %d want %d", settings.Port, port)
			}
			return newServer(settings, server.WithListener(listener), server.WithExitSignal(func() <-chan error { return exit }), server.WithExitWaitTime(10*time.Millisecond))
		}, registrations...)
	}()
	defer func() {
		exit <- nil
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
			rebound, err := net.Listen("tcp", address)
			if err != nil {
				t.Errorf("listener was not released after shutdown: %v", err)
			} else {
				_ = rebound.Close()
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not shut down")
		}
	}()
	for name, cli := range clients {
		deadline := time.Now().Add(5 * time.Second)
		for {
			response, err := cli.GenericCall(context.Background(), "Get", `{"key":"42"}`)
			if err == nil {
				if strings.Trim(fmt.Sprint(response), `"`) != name {
					t.Fatalf("%s received another service's response: %v", name, response)
				}
				break
			}
			select {
			case startupErr := <-done:
				// 保留结果交给统一清理检查，避免二次等待。
				done <- startupErr
				t.Fatalf("server exited before request: %v", startupErr)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s RPC failed: %v", name, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestOccupiedPortReturnsStartupError 验证配置端口被占用时返回启动错误。
func TestOccupiedPortReturnsStartupError(t *testing.T) {
	occupied, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	codec := testCodec(t, "ProductService")
	registration := NewRegistration(func(s server.Server, handler *generic.ServiceV2, options ...server.RegisterOption) error {
		return s.RegisterService(generic.ServiceInfoWithGeneric(codec), handler, options...)
	}, &generic.ServiceV2{})
	unexpectedStart := errors.New("occupied port unexpectedly accepted")
	exit := make(chan error, 1)
	exit <- unexpectedStart
	err = run(func() server.Server {
		return newServer(config.Settings{ServiceName: "test-api", Port: occupied.Addr().(*net.TCPAddr).Port}, server.WithExitSignal(func() <-chan error { return exit }))
	}, registration)
	if err == nil || errors.Is(err, unexpectedStart) {
		t.Fatal("server accepted an occupied port")
	}
}
