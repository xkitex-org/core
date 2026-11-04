package startup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
	"go.chaoxizhongzhi.com/xkitex/cache"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"go.chaoxizhongzhi.com/xkitex/database"
)

const cacheApplication = `cache:
  default: primary
  sources:
    primary: {type: valkey, address: 'localhost:6379'}
    session: {type: redis, address: 'localhost:6380'}
`

// recordingCache 包装真实缓存模块，记录配置隔离、资源启动和关闭次序。
type recordingCache struct {
	cache.Controller
	body    []byte
	order   *[]string
	failure error
}

// ConfigureCache 记录 cache 节，不接受其他模块的配置正文。
func (c *recordingCache) ConfigureCache(body []byte) error {
	c.body = body
	if c.failure != nil {
		return c.failure
	}
	return c.Controller.ConfigureCache(body)
}

// Start 创建真实 SDK 句柄但不联系服务器。
func (c *recordingCache) Start(ctx context.Context) error {
	*c.order = append(*c.order, "cache-start")
	return c.Controller.Start(ctx)
}

// Close 记录缓存释放时机。
func (c *recordingCache) Close() error {
	*c.order = append(*c.order, "cache-close")
	return c.Controller.Close()
}

// TestDatabaseAndCacheLifecycleTogether 验证两个模块单次配置、独立健康名称及排空后的逆序释放。
func TestDatabaseAndCacheLifecycleTogether(t *testing.T) {
	var order []string
	d := &recordingDatabase{Controller: database.New(), order: &order}
	c := &recordingCache{Controller: cache.New(), order: &order}
	p := &dependencyMonitor{order: &order}
	var reads atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(databaseApplication + cacheApplication)}})
	}))
	defer consul.Close()
	registration := NewRegistration(func(s server.Server, h *int, _ ...server.RegisterOption) error {
		return s.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "Product"}, h)
	}, new(int))
	err := startWithResources(context.Background(), configEnvironment(consul.URL), func(_ context.Context, _ config.Settings, state *lifecycle.State, _ <-chan error) server.Server {
		order = append(order, "rpc-create")
		if _, err := Cache().WithDataSource("session"); err != nil {
			t.Fatal(err)
		}
		if len(p.checks) != 4 || p.checks["cache.primary"] == nil || p.checks["database.primary"] == nil || state.Available() {
			t.Fatal("dependency readiness was bypassed or replaced")
		}
		return &databaseServer{fakeServer: &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}, onStop: func() {
			order = append(order, "rpc-stop")
			if _, err := Cache().WithDataSource("primary"); err != nil {
				t.Fatal("cache closed before RPC drain")
			}
			if _, err := Database().WithDataSource("primary"); err != nil {
				t.Fatal("database closed before RPC drain")
			}
		}}
	}, nil, NewMonitor(p), NewDatabase(d), NewCache(c), registration)
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || strings.Contains(string(c.body), "secret-marker") || strings.Contains(string(c.body), "database:") {
		t.Fatal("configuration sections leaked or were fetched twice")
	}
	want := []string{"database-start", "cache-start", "rpc-create", "rpc-stop", "monitor-close", "cache-close", "database-close"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order: %v", order)
	}
	if err := Cache().Get(context.Background(), "key").Err(); err == nil {
		t.Fatal("cache remained bound after stop")
	}
}

// TestCacheFailurePreventsRPCAndCloses 验证配置和检查协议错误均阻止启动并关闭客户端。
func TestCacheFailurePreventsRPCAndCloses(t *testing.T) {
	for _, badConfig := range []bool{true, false} {
		var order []string
		c := &recordingCache{Controller: cache.New(), order: &order}
		if badConfig {
			c.failure = errors.New("invalid cache configuration")
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(cacheApplication)}})
		}))
		created := false
		err := startWithResources(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
			created = true
			return &fakeServer{}
		}, nil, NewMonitor(&testMonitor{}), nil, NewCache(c))
		consul.Close()
		if err == nil || created || order[len(order)-1] != "cache-close" {
			t.Fatalf("failure leaked clients or started RPC: %v %v", err, order)
		}
		if badConfig && !errors.Is(err, c.failure) {
			t.Fatal("configuration error lost")
		}
	}
}

// TestCacheWithoutMonitorDoesNotPing 验证无探针时不增加连通性等待，且仍正确释放连接池。
func TestCacheWithoutMonitorDoesNotPing(t *testing.T) {
	var order []string
	c := &recordingCache{Controller: cache.New(), order: &order}
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(cacheApplication)}})
	}))
	defer consul.Close()
	failure := errors.New("registration reached")
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return failure }, new(int))
	err := startWithResources(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
		return &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	}, nil, nil, nil, NewCache(c), registration)
	if !errors.Is(err, failure) || !reflect.DeepEqual(order, []string{"cache-start", "cache-close"}) {
		t.Fatalf("unexpected startup ping or cleanup: %v %v", err, order)
	}
}

// TestCacheBindingCannotBeOverwritten 验证无参数入口不会被第二个应用静默覆盖。
func TestCacheBindingCannotBeOverwritten(t *testing.T) {
	first, second := cache.New(), cache.New()
	release, err := bindCache(first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := bindCache(second); err == nil {
		t.Fatal("cache binding overwritten")
	}
	applicationCache.RLock()
	defer applicationCache.RUnlock()
	if applicationCache.current != first {
		t.Fatal("application owner changed")
	}
}
