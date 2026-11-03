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

	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
)

// TestServiceLogsDistinguishPreparationRegistrationAndExit 验证日志明确区分配置生效、IDL 注册、运行及退出，失败时不误报正常停止。
func TestServiceLogsDistinguishPreparationRegistrationAndExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		runErr error
		want   string
	}{
		{"normal exit", "service: {port: 9088}", nil, "服务已停止：name=[demo-api]"},
		{"run failure", "service: {port: 9088}", errors.New("bind failed"), "服务启动或运行失败：name=[demo-api]"},
		{"configuration failure", "service: {port: 0}", nil, "配置加载失败："},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			klog.SetOutput(&output)
			t.Cleanup(func() { klog.SetOutput(os.Stderr) })
			consul := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"Value": []byte(tc.body)}})
			}))
			defer consul.Close()
			s := &fakeServer{services: make(map[string]*serviceinfo.ServiceInfo), runErr: tc.runErr}
			registration := func(name string) Registration {
				return NewRegistration(func(actual server.Server, handler *int, _ ...server.RegisterOption) error {
					return actual.RegisterService(&serviceinfo.ServiceInfo{ServiceName: name}, handler)
				}, new(int))
			}
			_ = start(context.Background(), configEnvironment(consul.URL), func(config.Settings) server.Server { return s }, registration("ProductService"), registration("CategoryService"))
			logs := output.String()
			if !strings.Contains(logs, tc.want) {
				t.Fatalf("missing expected startup stage: %s", tc.want)
			}
			if tc.name == "configuration failure" {
				if strings.Contains(logs, "准备启动服务") || strings.Contains(logs, "IDL Service 注册完成") || s.runs != 0 {
					t.Fatal("configuration failure reported server preparation or registration")
				}
				return
			}
			stages := []string{"服务配置生效", "准备启动服务：name=[demo-api] listen=[:9088]", "IDL Service 注册完成：services=[CategoryService, ProductService]", tc.want}
			previous := -1
			for _, stage := range stages {
				index := strings.Index(logs, stage)
				if index <= previous {
					t.Fatalf("startup stage missing or out of order: %s", stage)
				}
				previous = index
			}
			if tc.runErr != nil && strings.Contains(logs, "服务已停止：") {
				t.Fatal("failed run was reported as a normal exit")
			}
		})
	}
}
