package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/hashicorp/consul/api"
)

// consulReader 在一次启动加载期间复用官方 SDK 客户端，不创建配置监听。
type consulReader struct {
	client *api.Client
	token  string
}

// newConsulReader 创建启动期读取器，并返回释放其空闲连接的清理函数。
func newConsulReader(endpoint *url.URL, token string) (*consulReader, func(), error) {
	// 独立传输器，关闭启动期空闲连接，不影响应用自己的 HTTP 连接池。
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 5 * time.Second}
	client, err := api.NewClient(&api.Config{
		Address: endpoint.Host,
		Scheme:  endpoint.Scheme,
		Token:   token,
		HttpClient: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			// 不跟随重定向，确保 ACL Token 只发送到配置的 Consul 端点。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, fmt.Errorf("core: create Consul configuration client failed")
	}
	return &consulReader{client: client, token: token}, transport.CloseIdleConnections, nil
}

// read 单次读取明确的 KV Key，失败时不返回配置正文或 SDK 响应正文。
func (r *consulReader) read(ctx context.Context, key string) ([]byte, error) {
	// 不设置阻塞查询参数，不创建 Watch；只在启动时同步读取一次。
	pair, _, err := r.client.KV().Get(key, (&api.QueryOptions{Token: r.token}).WithContext(ctx))
	if err != nil {
		var status api.StatusError
		switch {
		case errors.As(err, &status):
			// SDK 的 StatusError 含响应正文，只保留状态码。
			return nil, fmt.Errorf("core: read Consul configuration %q: HTTP %d", key, status.Code)
		case errors.Is(err, context.Canceled):
			return nil, fmt.Errorf("core: read Consul configuration %q: %w", key, context.Canceled)
		case errors.Is(err, context.DeadlineExceeded):
			return nil, fmt.Errorf("core: read Consul configuration %q: %w", key, context.DeadlineExceeded)
		default:
			// SDK 的解码错误也可能包含配置值，不向外暴露原始错误。
			return nil, fmt.Errorf("core: read Consul configuration %q failed", key)
		}
	}
	if pair == nil {
		return nil, fmt.Errorf("core: Consul configuration %q does not exist", key)
	}
	if len(pair.Value) > maxConfigBytes {
		return nil, fmt.Errorf("core: Consul configuration exceeds 1 MiB")
	}
	return pair.Value, nil
}
