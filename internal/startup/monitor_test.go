package startup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testMonitor 记录配置隔离和资源顺序，检查本身由独立 monitor 模块测试。
type testMonitor struct {
	body                        []byte
	serviceName                 string
	configured, started, closed int
	configErr, startErr         error
	phase                       func() string
	report                      func(bool, bool)
	fatal                       chan error
}

// SetServiceName 记录可选日志身份协议收到的已校验服务名。
func (p *testMonitor) SetServiceName(name string) { p.serviceName = name }

// ConfigureMonitor 记录 monitor 节。
func (p *testMonitor) ConfigureMonitor(body []byte) error {
	p.configured++
	p.body = body
	return p.configErr
}

// Start 保存每实例回调，不启动真实 HTTP。
func (p *testMonitor) Start(_ context.Context, phase func() string, report func(bool, bool)) (<-chan error, error) {
	p.started++
	p.phase, p.report = phase, report
	p.fatal = make(chan error, 1)
	return p.fatal, p.startErr
}

// Close 记录关闭发生在 RPC 终止之后。
func (p *testMonitor) Close() error { p.closed++; return nil }

// TestMonitorSectionAndLifecycleIsolation 验证只传 monitor 节、初始不可用、恢复可用及停机锁存。
func TestMonitorSectionAndLifecycleIsolation(t *testing.T) {
	p := &testMonitor{}
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("monitor: {port: 9091}\ndatabase: {password: private-value}")}})
	}))
	defer consul.Close()
	sentinel := errors.New("registration stopped")
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return sentinel }, new(int))
	var state *lifecycle.State
	err := startWithComponents(context.Background(), configEnvironment(consul.URL), func(_ context.Context, _ config.Settings, s *lifecycle.State, _ <-chan error) server.Server {
		state = s
		if p.serviceName != "demo-api" || p.started != 1 || p.phase() != "Starting" || s.Available() {
			t.Fatal("wrong initial lifecycle")
		}
		s.Run()
		p.report(true, true)
		if !s.Available() {
			t.Fatal("checks did not enable availability")
		}
		s.Stop()
		p.report(true, true)
		if s.Available() {
			t.Fatal("late report revived stopping service")
		}
		return &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	}, nil, NewMonitor(p), registration)
	if !errors.Is(err, sentinel) || p.closed != 1 || p.configured != 1 || strings.Contains(string(p.body), "private-value") || !strings.Contains(string(p.body), "port:") || state.Phase() != "Failed" {
		t.Fatalf("invalid monitor lifecycle: %v", err)
	}
}

// TestMonitorFailurePreventsRPCAndStillCloses 验证配置或 HTTP 启动失败阻止 RPC 创建并清理探针。
func TestMonitorFailurePreventsRPCAndStillCloses(t *testing.T) {
	for _, stage := range []string{"configure", "start"} {
		p := &testMonitor{}
		failure := errors.New("monitor failed")
		if stage == "configure" {
			p.configErr = failure
		} else {
			p.startErr = failure
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("{}")}})
		}))
		var created atomic.Bool
		err := startWithComponents(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
			created.Store(true)
			return &fakeServer{}
		}, nil, NewMonitor(p))
		consul.Close()
		if !errors.Is(err, failure) || created.Load() || p.closed != 1 {
			t.Fatalf("failure did not prevent RPC or close resources: %v", err)
		}
	}
}

// TestMonitorFatalSignalRevokesAvailability 验证探针服务故障先撤销可用性，再通知 Kitex 退出。
func TestMonitorFatalSignalRevokesAvailability(t *testing.T) {
	state := lifecycle.New("demo-api", true)
	state.Run()
	state.Report(true, true)
	ctx, exit, start, cleanup := supervise(context.Background(), state)
	defer cleanup()
	fatal := make(chan error, 1)
	start(fatal)
	failure := errors.New("HTTP stopped")
	fatal <- failure
	select {
	case err := <-exit:
		if !errors.Is(err, failure) || state.Available() || state.Phase() != "Failed" || ctx.Err() == nil {
			t.Fatal("fatal notification ordering incorrect")
		}
	case <-time.After(time.Second):
		t.Fatal("fatal notification did not reach runtime")
	}
}
