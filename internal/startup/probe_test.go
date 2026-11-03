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

// testProbe 记录配置隔离和资源顺序，检查本身由独立 probe 模块测试。
type testProbe struct {
	body                        []byte
	configured, started, closed int
	configErr, startErr         error
	phase                       func() string
	report                      func(bool, bool)
	fatal                       chan error
}

// ConfigureProbe 记录 probe 节。
func (p *testProbe) ConfigureProbe(body []byte) error {
	p.configured++
	p.body = body
	return p.configErr
}

// Start 保存每实例回调，不启动真实 HTTP。
func (p *testProbe) Start(_ context.Context, phase func() string, report func(bool, bool)) (<-chan error, error) {
	p.started++
	p.phase, p.report = phase, report
	p.fatal = make(chan error, 1)
	return p.fatal, p.startErr
}

// Close 记录关闭发生在 RPC 终止之后。
func (p *testProbe) Close() error { p.closed++; return nil }

// TestProbeSectionAndLifecycleIsolation 验证只传 probe 节、初始不可用、恢复可用及停机锁存。
func TestProbeSectionAndLifecycleIsolation(t *testing.T) {
	p := &testProbe{}
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("probe: {port: 22222}\ndatabase: {password: private-value}")}})
	}))
	defer consul.Close()
	sentinel := errors.New("registration stopped")
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return sentinel }, new(int))
	var state *lifecycle.State
	err := startWithComponents(context.Background(), configEnvironment(consul.URL), func(_ config.Settings, s *lifecycle.State, _ <-chan error) server.Server {
		state = s
		if p.started != 1 || p.phase() != "Starting" || s.Available() {
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
	}, nil, NewProbe(p), registration)
	if !errors.Is(err, sentinel) || p.closed != 1 || p.configured != 1 || strings.Contains(string(p.body), "private-value") || !strings.Contains(string(p.body), "port:") || state.Phase() != "Failed" {
		t.Fatalf("invalid probe lifecycle: %v", err)
	}
}

// TestProbeFailurePreventsRPCAndStillCloses 验证配置或 HTTP 启动失败阻止 RPC 创建并清理探针。
func TestProbeFailurePreventsRPCAndStillCloses(t *testing.T) {
	for _, stage := range []string{"configure", "start"} {
		p := &testProbe{}
		failure := errors.New("probe failed")
		if stage == "configure" {
			p.configErr = failure
		} else {
			p.startErr = failure
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("{}")}})
		}))
		var created atomic.Bool
		err := startWithComponents(context.Background(), configEnvironment(consul.URL), func(config.Settings, *lifecycle.State, <-chan error) server.Server {
			created.Store(true)
			return &fakeServer{}
		}, nil, NewProbe(p))
		consul.Close()
		if !errors.Is(err, failure) || created.Load() || p.closed != 1 {
			t.Fatalf("failure did not prevent RPC or close resources: %v", err)
		}
	}
}

// TestProbeFatalSignalRevokesAvailability 验证探针服务故障先撤销可用性，再通知 Kitex 退出。
func TestProbeFatalSignalRevokesAvailability(t *testing.T) {
	state := lifecycle.New(true)
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
