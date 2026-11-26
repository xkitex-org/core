package registration

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/cloudwego/kitex/pkg/registry"
)

// instanceInfo 将实际监听地址固定为单个实例地址，保留既有服务实例 ID 格式。
func instanceInfo(source *registry.Info, name, owner string) (*registry.Info, string, error) {
	if source == nil || source.Addr == nil || source.ServiceName != name {
		return nil, "", fmt.Errorf("core: invalid Consul instance registration")
	}
	host, portText, err := net.SplitHostPort(source.Addr.String())
	if err != nil {
		return nil, "", fmt.Errorf("core: invalid RPC registration address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, "", fmt.Errorf("core: invalid RPC registration port")
	}
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return nil, "", fmt.Errorf("core: cannot determine RPC registration address")
		}
		host = ""
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok && !network.IP.IsLoopback() && network.IP.To4() != nil {
				host = network.IP.To4().String()
				break
			}
		}
		if host == "" {
			return nil, "", fmt.Errorf("core: no local IPv4 address for Consul registration")
		}
	}
	address, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, portText))
	if err != nil || address.IP == nil {
		return nil, "", fmt.Errorf("core: invalid RPC registration address")
	}
	result := *source
	result.Addr = address
	result.Tags = make(map[string]string, len(source.Tags)+2)
	for key, value := range source.Tags {
		if strings.Contains(key, ":") {
			return nil, "", fmt.Errorf("core: invalid Consul registration tag")
		}
		result.Tags[key] = value
	}
	result.Tags[managedTag], result.Tags[ownerTag] = "true", owner
	if result.Weight == 0 {
		result.Weight = 10
	}
	return &result, fmt.Sprintf("%s:%s:%d", name, address.IP.String(), port), nil
}
