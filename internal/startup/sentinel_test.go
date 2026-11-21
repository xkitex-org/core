package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"github.com/hashicorp/consul/api"
	"go.chaoxizhongzhi.com/xkitex/cache"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"go.chaoxizhongzhi.com/xkitex/database"
	"go.chaoxizhongzhi.com/xkitex/sentinel"
)

const sentinelApplication = "sentinel:\n  rule_prefix: config/demo/api/sentinel\n"

// recordingSentinel 记录配置隔离、共享 SDK 客户端及清理时机，不初始化真实全局 SDK。
type recordingSentinel struct {
	body                []byte
	order               *[]string
	client              *api.Client
	service             string
	ctx                 context.Context
	configErr, startErr error
	closed              int
}

// ConfigureSentinel 只接收 sentinel 节，不重读普通配置。
func (s *recordingSentinel) ConfigureSentinel(body []byte) error { s.body = body; return s.configErr }

// Start 使用 core 下发的同一 Consul 客户端读取测试规则，确认 ACL 与应用身份。
func (s *recordingSentinel) Start(ctx context.Context, client *api.Client, service string) error {
	*s.order = append(*s.order, "sentinel-start")
	s.client, s.service, s.ctx = client, service, ctx
	if s.startErr != nil {
		return s.startErr
	}
	_, _, err := client.KV().Get("config/demo/api/sentinel/flow", (&api.QueryOptions{}).WithContext(ctx))
	return err
}

// Middleware 返回纯透传测试替身，真实 SDK 放行及阻断另由模块与 RPC 测试验证。
func (*recordingSentinel) Middleware() endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint { return next }
}

// Close 记录 RPC 排空之后的监听清理。
func (s *recordingSentinel) Close() error {
	*s.order = append(*s.order, "sentinel-close")
	s.closed++
	return nil
}

// TestSentinelUsesSameConfigurationAndConsul 验证基础配置单次下发、客户端复用和完整资源释放顺序。
func TestSentinelUsesSameConfigurationAndConsul(t *testing.T) {
	var order []string
	g := &recordingSentinel{order: &order}
	d := &recordingDatabase{Controller: database.New(), order: &order}
	c := &recordingCache{Controller: cache.New(), order: &order}
	p := &dependencyMonitor{order: &order}
	var configs, rules atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Consul-Token") != "test-token" {
			t.Error("ACL snapshot was not reused")
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/v1/kv/config/demo/api/application.yaml":
			configs.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(databaseApplication + cacheApplication + sentinelApplication)}})
		case "/v1/kv/config/demo/api/sentinel/flow":
			rules.Add(1)
			w.Header().Set("X-Consul-Index", "1")
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte("[]")}})
		default:
			t.Error("unexpected configuration key")
			w.WriteHeader(404)
		}
	}))
	defer consul.Close()
	registration := NewRegistration(func(s server.Server, h *int, _ ...server.RegisterOption) error {
		return s.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "Product"}, h)
	}, new(int))
	err := startWithSentinel(context.Background(), configEnvironment(consul.URL), func(_ context.Context, settings config.Settings, _ *lifecycle.State, _ <-chan error) server.Server {
		order = append(order, "rpc-create")
		if g.client != settings.ConsulClient() || g.service != "demo-api" {
			t.Fatal("Sentinel got another client or identity")
		}
		return &databaseServer{fakeServer: &fakeServer{services: map[string]*serviceinfo.ServiceInfo{}}, onStop: func() {
			order = append(order, "rpc-stop")
			if g.closed != 0 {
				t.Fatal("Sentinel stopped before RPC drained")
			}
		}}
	}, nil, NewMonitor(p), NewDatabase(d), NewCache(c), NewSentinel(g), registration)
	if err != nil {
		t.Fatal(err)
	}
	if configs.Load() != 1 || rules.Load() != 1 || strings.Contains(string(g.body), "secret-marker") || strings.Contains(string(g.body), "cache:") {
		t.Fatal("ordinary configuration reloaded or leaked")
	}
	want := []string{"database-start", "cache-start", "sentinel-start", "rpc-create", "rpc-stop", "sentinel-close", "monitor-close", "cache-close", "database-close"}
	if !reflect.DeepEqual(order, want) || g.closed != 1 || g.ctx.Err() == nil {
		t.Fatalf("cleanup order or cancellation: %v", order)
	}
}

// TestSentinelStartupFailurePreventsRPC 验证配置及初始规则错误阻止服务创建，且收尾只执行一次。
func TestSentinelStartupFailurePreventsRPC(t *testing.T) {
	for _, stage := range []string{"configure", "start"} {
		var order []string
		g := &recordingSentinel{order: &order}
		failure := errors.New("guard initialization failed")
		if stage == "configure" {
			g.configErr = failure
		} else {
			g.startErr = failure
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(sentinelApplication)}})
		}))
		created := false
		err := startWithSentinel(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
			created = true
			return &fakeServer{}
		}, nil, nil, nil, nil, NewSentinel(g))
		consul.Close()
		if !errors.Is(err, failure) || created || g.closed != 1 {
			t.Fatalf("startup failure handling: %v created=%v close=%d", err, created, g.closed)
		}
	}
}

// TestSentinelBindingRejectsReuseWithoutClosingOwner 验证第二次启动不会配置或关闭正在使用的同一个控制器。
func TestSentinelBindingRejectsReuseWithoutClosingOwner(t *testing.T) {
	release, err := bindSentinel()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var order []string
	g := &recordingSentinel{order: &order}
	err = startWithSentinel(context.Background(), configEnvironment("http://127.0.0.1:1"), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
		t.Error("duplicate owner reached RPC")
		return &fakeServer{}
	}, nil, nil, nil, nil, NewSentinel(g))
	if err == nil || g.closed != 0 || len(order) != 0 {
		t.Fatalf("duplicate owner damaged active controller: %v %v", err, order)
	}
}

func TestSentinelRulePrefixDefaultsToApplicationDirectory(t *testing.T) {
	for _, tt := range []struct {
		name, application, prefix string
	}{
		{"omitted", "{}\n", "config/demo/api/sentinel"},
		{"empty section", "sentinel: {}\n", "config/demo/api/sentinel"},
		{"explicit override", "sentinel: {rule_prefix: config/shared/sentinel}\n", "config/shared/sentinel"},
		{"imported override", "imports: [config/shared/application.yaml]\n", "config/shared/sentinel"},
		{"invalid explicit value", "sentinel: {rule_prefix: ''}\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reads sync.Map
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.URL.Path, "/v1/kv/")
				var body string
				switch {
				case key == "config/demo/api/application.yaml":
					body = tt.application
				case key == "config/shared/application.yaml":
					body = "sentinel: {rule_prefix: config/shared/sentinel}\n"
				case tt.prefix != "" && strings.HasPrefix(key, tt.prefix+"/"):
					reads.Store(key, true)
					body = "[]"
				default:
					t.Errorf("unexpected Consul Key: %s", key)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("X-Consul-Index", "1")
				_ = json.NewEncoder(w).Encode([]map[string]any{{"Key": key, "Value": []byte(body)}})
			}))
			t.Cleanup(consul.Close)
			registration := NewRegistration(func(s server.Server, h *int, _ ...server.RegisterOption) error {
				return s.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "Product"}, h)
			}, new(int))
			created := false
			err := startWithSentinel(context.Background(), configEnvironment(consul.URL), func(_ context.Context, settings config.Settings, _ *lifecycle.State, _ <-chan error) server.Server {
				created = true
				if settings.ServiceName != "demo-api" {
					t.Errorf("unexpected service name: %s", settings.ServiceName)
				}
				return &fakeServer{services: map[string]*serviceinfo.ServiceInfo{}}
			}, nil, nil, nil, nil, NewSentinel(sentinel.New()), registration)
			if tt.prefix == "" {
				if err == nil || created || !strings.Contains(err.Error(), "rule_prefix") {
					t.Fatalf("invalid explicit prefix must stop startup, got %v", err)
				}
				return
			}
			if err != nil || !created {
				t.Fatalf("startup failed: %v", err)
			}
			for _, kind := range []string{"flow", "circuitBreaker", "isolation", "system"} {
				key := tt.prefix + "/" + kind
				if _, ok := reads.Load(key); !ok {
					t.Errorf("rule not read: %s", key)
				}
			}
		})
	}
}
