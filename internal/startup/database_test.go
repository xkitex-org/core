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
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"go.chaoxizhongzhi.com/xkitex/database"
)

const databaseApplication = `database:
  default: primary
  sources:
    primary: {type: postgresql, host: localhost, database: zmall, username: zmall}
    second: {type: mysql, host: localhost, database: report, username: report}
monitor: {port: 9091}
logging: {private: secret-marker}
`

// recordingDatabase 包装真实模块，记录启动、配置隔离和资源关闭。
type recordingDatabase struct {
	database.Controller
	body    []byte
	order   *[]string
	failure error
}

// ConfigureDatabase 记录只有 database 节被传入。
func (d *recordingDatabase) ConfigureDatabase(body []byte) error {
	d.body = body
	if d.failure != nil {
		return d.failure
	}
	return d.Controller.ConfigureDatabase(body)
}

// Start 按真实模块创建连接池，并记录早于 RPC。
func (d *recordingDatabase) Start(ctx context.Context) error {
	*d.order = append(*d.order, "database-start")
	return d.Controller.Start(ctx)
}

// Close 记录实际数据库释放位置。
func (d *recordingDatabase) Close() error {
	*d.order = append(*d.order, "database-close")
	return d.Controller.Close()
}

// dependencyMonitor 记录 core 自动挂载的检查，不反向调用真实数据库。
type dependencyMonitor struct {
	testMonitor
	checks map[string]func(context.Context) error
	order  *[]string
}

// AddReadiness 接收独立的具名依赖检查。
func (p *dependencyMonitor) AddReadiness(name string, check func(context.Context) error) error {
	if p.checks == nil {
		p.checks = make(map[string]func(context.Context) error)
	}
	p.checks[name] = check
	return nil
}

// Close 记录探针停止先于数据库关闭。
func (p *dependencyMonitor) Close() error {
	*p.order = append(*p.order, "monitor-close")
	return p.testMonitor.Close()
}

// databaseServer 在模拟 RPC 排空时确认连接池尚未释放。
type databaseServer struct {
	*fakeServer
	onStop func()
}

// Stop 记录 RPC 排空并保留原测试服务器行为。
func (s *databaseServer) Stop() error { s.onStop(); return s.fakeServer.Stop() }

// TestDatabaseConfigurationAndDrainOrder 验证具名池初始化、自动探针、单次读取及停机顺序。
func TestDatabaseConfigurationAndDrainOrder(t *testing.T) {
	var order []string
	d := &recordingDatabase{Controller: database.New(), order: &order}
	p := &dependencyMonitor{order: &order}
	var reads atomic.Int32
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(databaseApplication)}})
	}))
	defer consul.Close()
	registration := NewRegistration(func(s server.Server, handler *int, _ ...server.RegisterOption) error {
		return s.RegisterService(&serviceinfo.ServiceInfo{ServiceName: "Product"}, handler)
	}, new(int))
	err := startWithDatabase(context.Background(), configEnvironment(consul.URL), func(_ context.Context, _ config.Settings, state *lifecycle.State, _ <-chan error) server.Server {
		order = append(order, "rpc-create")
		if _, err := Database().WithDataSource("second"); err != nil {
			t.Fatal(err)
		}
		if p.checks["database.primary"] == nil || p.checks["database.second"] == nil || state.Available() {
			t.Fatal("database checks were missing or readiness bypassed")
		}
		return &databaseServer{fakeServer: &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}, onStop: func() {
			order = append(order, "rpc-stop")
			if _, err := Database().WithDataSource("primary"); err != nil {
				t.Fatal("pool closed before RPC drain")
			}
		}}
	}, nil, NewMonitor(p), NewDatabase(d), registration)
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || strings.Contains(string(d.body), "secret-marker") || strings.Contains(string(d.body), "monitor:") {
		t.Fatal("configuration was re-read or sections leaked")
	}
	if !reflect.DeepEqual(order, []string{"database-start", "rpc-create", "rpc-stop", "monitor-close", "database-close"}) {
		t.Fatalf("cleanup ordering: %v", order)
	}
	if _, err := Database().WithDataSource("primary"); err == nil {
		t.Fatal("application database remained globally bound")
	}
}

// TestDatabaseFailureAndMissingMonitorProtocol 验证错误路径释放资源且不创建 RPC，不悄悄跳过健康检查。
func TestDatabaseFailureAndMissingMonitorProtocol(t *testing.T) {
	for _, configFailure := range []bool{true, false} {
		var order []string
		d := &recordingDatabase{Controller: database.New(), order: &order}
		if configFailure {
			d.failure = errors.New("invalid database")
		}
		consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(databaseApplication)}})
		}))
		created := false
		err := startWithDatabase(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
			created = true
			return &fakeServer{}
		}, nil, NewMonitor(&testMonitor{}), NewDatabase(d))
		consul.Close()
		if err == nil || created || order[len(order)-1] != "database-close" {
			t.Fatalf("failed startup leaked resources: %v %v", err, order)
		}
		if configFailure && !errors.Is(err, d.failure) {
			t.Fatal("configuration error lost")
		}
	}
}

// TestNoMonitorDoesNotRunDependencyChecks 验证无 monitor 时连接池只创建句柄，不增加连通性等待。
func TestNoMonitorDoesNotRunDependencyChecks(t *testing.T) {
	var order []string
	d := &recordingDatabase{Controller: database.New(), order: &order}
	consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(databaseApplication)}})
	}))
	defer consul.Close()
	failure := errors.New("registration reached")
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return failure }, new(int))
	err := startWithDatabase(context.Background(), configEnvironment(consul.URL), func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server {
		return &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	}, nil, nil, NewDatabase(d), registration)
	if !errors.Is(err, failure) {
		t.Fatalf("unexpected connection check without monitor: %v", err)
	}
}

// TestDatabaseBindingIsExclusive 验证第二个管理器不会覆盖或关闭已归属当前应用的数据源。
func TestDatabaseBindingIsExclusive(t *testing.T) {
	first, second := database.New(), database.New()
	release, err := bindDatabase(first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := bindDatabase(second); err == nil {
		t.Fatal("application binding overwritten")
	}
	applicationDatabase.RLock()
	defer applicationDatabase.RUnlock()
	if applicationDatabase.current != first {
		t.Fatal("active application database was replaced")
	}
}
