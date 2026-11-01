package startup

import (
	"errors"
	"testing"

	"github.com/cloudwego/kitex/pkg/serviceinfo"
	"github.com/cloudwego/kitex/server"
)

type fakeServer struct {
	services map[string]*serviceinfo.ServiceInfo
	runs     int
	stops    int
	runErr   error
	stopErr  error
}

// RegisterService 将测试 Service 记录到共享 Server 中。
func (s *fakeServer) RegisterService(info *serviceinfo.ServiceInfo, _ any, _ ...server.RegisterOption) error {
	s.services[info.ServiceName] = info
	return nil
}

// GetServiceInfos 返回测试 Server 中已注册的 Service。
func (s *fakeServer) GetServiceInfos() map[string]*serviceinfo.ServiceInfo { return s.services }

// Run 记录运行次数并返回测试指定的运行错误。
func (s *fakeServer) Run() error { s.runs++; return s.runErr }

// Stop 记录停止次数并返回测试指定的收尾错误。
func (s *fakeServer) Stop() error { s.stops++; return s.stopErr }

// TestServicesShareOneServerAndRegisterBeforeRun 验证多个 Service 在运行前依次注册到同一 Server。
func TestServicesShareOneServerAndRegisterBeforeRun(t *testing.T) {
	s := &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	created := 0
	var order []string
	registration := func(name string) Registration {
		return NewRegistration(func(actual server.Server, handler *int, _ ...server.RegisterOption) error {
			if actual != s || s.runs != 0 {
				t.Fatal("services do not share the same unstarted server")
			}
			order = append(order, name)
			return actual.RegisterService(&serviceinfo.ServiceInfo{ServiceName: name}, handler)
		}, new(int))
	}
	err := run(func() server.Server { created++; return s }, registration("Product"), registration("Inventory"))
	if err != nil || created != 1 || s.runs != 1 || s.stops != 1 || len(order) != 2 || order[0] != "Product" || order[1] != "Inventory" {
		t.Fatalf("unexpected run: %v, created=%d run=%d stop=%d order=%v", err, created, s.runs, s.stops, order)
	}
}

// TestRunAndStopErrorsArePreserved 验证运行错误和停止错误均被保留。
func TestRunAndStopErrorsArePreserved(t *testing.T) {
	runErr, stopErr := errors.New("bind failed"), errors.New("cleanup failed")
	s := &fakeServer{services: map[string]*serviceinfo.ServiceInfo{"Test": {}}, runErr: runErr, stopErr: stopErr}
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { return nil }, new(int))
	err := run(func() server.Server { return s }, registration)
	if !errors.Is(err, runErr) || !errors.Is(err, stopErr) || s.stops != 1 {
		t.Fatalf("startup did not preserve errors or clean up: %v", err)
	}
}

// TestRegistrationPanicDoesNotStartServer 验证注册 panic 转为错误后不会启动 Server。
func TestRegistrationPanicDoesNotStartServer(t *testing.T) {
	want := errors.New("registration panic")
	s := &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo)}
	registration := NewRegistration(func(server.Server, *int, ...server.RegisterOption) error { panic(want) }, new(int))
	err := run(func() server.Server { return s }, registration)
	if !errors.Is(err, want) || s.runs != 0 {
		t.Fatalf("registration panic escaped or server started: %v", err)
	}
}
