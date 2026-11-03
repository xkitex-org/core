package startup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"gopkg.in/yaml.v3"

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

// lifecycleLogger 在真实 klog 输出之外记录配置、运行及关闭顺序。
type lifecycleLogger struct {
	klog.FullLogger
	body                []byte
	configured          int
	closed              int
	configureErr        error
	closeErr            error
	output              *bytes.Buffer
	closedAfterFinalLog bool
}

// ConfigureLogging 记录收到的独立 logging 节，并返回测试指定的错误。
func (l *lifecycleLogger) ConfigureLogging(body []byte) error {
	l.body, l.configured = body, l.configured+1
	return l.configureErr
}

// Close 验证退出日志先于资源关闭，并记录关闭次数。
func (l *lifecycleLogger) Close() error {
	l.closed++
	l.closedAfterFinalLog = strings.Contains(l.output.String(), "服务已停止") && strings.Contains(l.output.String(), "to=[Stopped]")
	return l.closeErr
}

// loggingFixture 创建隔离的日志观察器，在每个测试结束后恢复全局 logger。
func loggingFixture(t *testing.T) *lifecycleLogger {
	t.Helper()
	previous := klog.DefaultLogger()
	var output bytes.Buffer
	previous.SetOutput(&output)
	t.Cleanup(func() { klog.SetLogger(previous); previous.SetOutput(os.Stderr) })
	return &lifecycleLogger{FullLogger: previous, output: &output}
}

// TestExplicitLoggerLifecycleAndMergedSection 验证安装早于 Consul 请求、配置只下发 logging 节且最终日志先于关闭。
func TestExplicitLoggerLifecycleAndMergedSection(t *testing.T) {
	l := loggingFixture(t)
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if klog.DefaultLogger() != l {
			t.Error("explicit logger was not installed before fetching configuration")
		}
		body := "logging: {level: debug, format: json}\ndatabase: {password: private-value}"
		if strings.HasSuffix(r.URL.Path, "application.yaml") {
			body = "imports: [config/demo/shared/base.yaml]\nlogging: {level: warn}"
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(body)}})
	}))
	defer consul.Close()
	s := &fakeServer{services: map[string]*serviceinfo.ServiceInfo{"TestService": {}}}
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return nil }, new(int))
	err := startWithLogging(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server {
		if l.configured != 1 || l.closed != 0 {
			t.Fatal("service created before logger configuration or after logger close")
		}
		return s
	}, NewLogging(l), registration)
	var actual map[string]any
	if decodeErr := yaml.Unmarshal(l.body, &actual); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || l.configured != 1 || l.closed != 1 || s.stops != 1 || actual["level"] != "warn" || actual["format"] != "json" || len(actual) != 2 {
		t.Fatalf("incorrect lifecycle or section: %v configured=%d closed=%d", err, l.configured, l.closed)
	}
	if strings.Contains(string(l.body), "private-value") || !l.closedAfterFinalLog {
		t.Fatal("unrelated configuration exposed or final log missing")
	}
}

// TestLoggerFailuresCloseResourcesAndPreventStartup 验证读取、日志配置和注册失败均关闭资源，关闭错误与原始错误一起保留。
func TestLoggerFailuresCloseResourcesAndPreventStartup(t *testing.T) {
	for _, stage := range []string{"fetch", "configure", "register", "close"} {
		t.Run(stage, func(t *testing.T) {
			l := loggingFixture(t)
			failure := errors.New("test failure")
			if stage == "configure" {
				l.configureErr = failure
			}
			if stage == "close" {
				l.closeErr = failure
			}
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if stage == "fetch" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("{}")}})
			}))
			defer consul.Close()
			s := &fakeServer{services: map[string]*serviceinfo.ServiceInfo{"Test": {}}}
			registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error {
				if stage == "register" {
					return failure
				}
				return nil
			}, new(int))
			err := startWithLogging(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server { return s }, NewLogging(l), registration)
			if err == nil || l.closed != 1 || (stage != "fetch" && !errors.Is(err, failure)) || (stage != "close" && s.runs != 0) {
				t.Fatalf("failed logger lifecycle: %v closed=%d runs=%d", err, l.closed, s.runs)
			}
			if stage == "fetch" && l.configured != 0 {
				t.Fatal("logger configured after failed read")
			}
			if stage != "fetch" && len(l.body) != 0 {
				t.Fatal("missing logging section was not passed as nil")
			}
		})
	}
}

// TestOmittedOptionLeavesExistingLoggerUntouched 验证即使全局 logger 有配置和关闭能力，未传选项也不会被 core 管理。
func TestOmittedOptionLeavesExistingLoggerUntouched(t *testing.T) {
	l := loggingFixture(t)
	klog.SetLogger(l)
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("logging: invalid-for-logger")}})
	}))
	defer consul.Close()
	s := &fakeServer{services: map[string]*serviceinfo.ServiceInfo{"Test": {}}}
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return nil }, new(int))
	if err := start(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server { return s }, registration); err != nil {
		t.Fatal(err)
	}
	if klog.DefaultLogger() != l || l.configured != 0 || l.closed != 0 {
		t.Fatal("omitted logger option changed global logger lifecycle")
	}
}

// syncOnlyLogger 模拟提供刷新能力但不支持 logging 配置和资源关闭的第三方实现。
type syncOnlyLogger struct {
	klog.FullLogger
	syncs int
}

// Sync 记录 core 在服务退出后执行的刷新次数。
func (l *syncOnlyLogger) Sync() error { l.syncs++; return nil }

// TestPlainAndSyncOnlyLoggers 验证普通 FullLogger 可直接使用，配置能力可选，Sync 只在没有 Close 时调用。
func TestPlainAndSyncOnlyLoggers(t *testing.T) {
	for _, withSync := range []bool{false, true} {
		l := loggingFixture(t)
		plain := struct{ klog.FullLogger }{l.FullLogger}
		syncer := &syncOnlyLogger{FullLogger: l.FullLogger}
		var selected klog.FullLogger = plain
		if withSync {
			selected = syncer
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("logging: invalid-for-logging-module")}})
		}))
		s := &fakeServer{services: map[string]*serviceinfo.ServiceInfo{"Test": {}}}
		registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return nil }, new(int))
		err := startWithLogging(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server { return s }, NewLogging(selected), registration)
		consul.Close()
		if err != nil || (withSync && syncer.syncs != 1) || s.runs != 1 {
			t.Fatalf("third-party logger rejected or not flushed: %v", err)
		}
		// 下一轮构造夹具前恢复原有全局实现，测试结束时的清理由夹具完成。
		klog.SetLogger(l.FullLogger)
	}
}
