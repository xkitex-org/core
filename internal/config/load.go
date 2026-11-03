package config

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/cloudwego/kitex/pkg/klog"
	"gopkg.in/yaml.v3"
)

const (
	defaultPort    = 8888
	maxConfigBytes = 1 << 20
)

// Settings 是 core 内部使用的启动配置，不作为业务公共 API。
type Settings struct {
	ServiceName string
	Port        int
}

// Load 在启动时读取固定入口及其引用配置，合并校验后返回服务启动参数。
func Load(ctx context.Context, lookup func(string) (string, bool)) (settings Settings, err error) {
	return LoadWithLogging(ctx, lookup, nil)
}

// LoadWithLogging 在全部配置合并和端口校验通过后，仅向显式传入的 logger 下发 logging 节。
func LoadWithLogging(ctx context.Context, lookup func(string) (string, bool), configure func([]byte) error) (settings Settings, err error) {
	defer func() {
		if err != nil {
			klog.Errorf("core: 配置加载失败：%v", err)
		}
	}()
	if lookup == nil {
		return Settings{}, fmt.Errorf("core: environment reader is nil")
	}
	id, err := loadIdentity(lookup)
	if err != nil {
		return Settings{}, err
	}
	endpoint, err := consulEndpoint(lookup)
	if err != nil {
		return Settings{}, err
	}
	rawToken, _ := lookup("X_CONSUL_TOKEN")
	token := strings.TrimSpace(rawToken)
	if token == "" {
		return Settings{}, fmt.Errorf("core: X_CONSUL_TOKEN is required")
	}
	if strings.ContainsAny(token, "\r\n") {
		return Settings{}, fmt.Errorf("core: X_CONSUL_TOKEN contains invalid characters")
	}
	klog.Infof("core: 启动配置来源：service=[%s] consul=[%s://%s] entry=[%s]", id.name, endpoint.Scheme, endpoint.Host, id.key)
	reader, closeReader, err := newConsulReader(endpoint, token)
	if err != nil {
		return Settings{}, err
	}
	defer closeReader()
	application, err := loadApplication(ctx, id.key, reader.read)
	if err != nil {
		return Settings{}, err
	}
	port, err := parsePort(application.values)
	if err != nil {
		return Settings{}, fmt.Errorf("core: configuration %q: %w", id.key, err)
	}
	if configure != nil {
		var body []byte
		if value, exists := application.values["logging"]; exists {
			body, err = yaml.Marshal(value)
			if err != nil {
				return Settings{}, fmt.Errorf("core: encode logging configuration failed")
			}
		}
		if err := configure(body); err != nil {
			return Settings{}, fmt.Errorf("core: configure logging: %w", err)
		}
	}
	application.logOverrides()
	portSource := "默认值"
	if service := application.sources["service"]; service != nil {
		if port := service.children["port"]; port != nil {
			portSource = port.file
		}
	}
	klog.Infof("core: 服务配置生效：name=[%s] port=[%d] port_source=[%s]", id.name, port, portSource)
	return Settings{ServiceName: id.name, Port: port}, nil
}

// consulEndpoint 读取并校验 Consul 地址，未声明协议时使用 HTTP。
func consulEndpoint(lookup func(string) (string, bool)) (*url.URL, error) {
	raw, _ := lookup("X_CONSUL_ADDR")
	address := strings.TrimSpace(raw)
	if address == "" {
		return nil, fmt.Errorf("core: X_CONSUL_ADDR is required")
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("core: X_CONSUL_ADDR must be an HTTP(S) Consul address")
	}
	if endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return nil, fmt.Errorf("core: X_CONSUL_ADDR must not contain credentials, a path, query or fragment")
	}
	return endpoint, nil
}

// parsePort 从合并后的配置提取服务端口，仅在端口缺省时使用默认值。
func parsePort(application map[string]any) (int, error) {
	value, exists := application["service"]
	if !exists {
		return defaultPort, nil
	}
	service, ok := value.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("service must be a YAML object")
	}
	value, exists = service["port"]
	if !exists {
		return defaultPort, nil
	}
	port, ok := value.(int)
	if !ok || port < 1 || port > 65535 {
		return 0, fmt.Errorf("service.port must be an integer between 1 and 65535")
	}
	return port, nil
}
