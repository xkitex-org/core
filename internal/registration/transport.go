package registration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// libraryTransport 限制第三方心跳生命周期，并去除第三方可能直接写入 klog 的错误正文。
type libraryTransport struct {
	base     http.RoundTripper
	ctx      context.Context
	mu       sync.Mutex
	closed   bool
	requests sync.WaitGroup
}

// RoundTrip 正常请求仍发送至已校验端点；本地收尾模式仅允许第三方执行自身取消逻辑。
func (t *libraryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		if request.Body != nil {
			_ = request.Body.Close()
		}
		if strings.HasPrefix(request.URL.Path, "/v1/agent/service/deregister/") {
			// registry-consul 仅在 Deregister 成功后取消私有心跳，没有独立 Close 方法。
			// 真实注销已在上层执行并保留其错误；这里不发网络请求，也不代表 Consul 注销成功。
			return localResponse(request), nil
		}
		return nil, context.Canceled
	}
	t.requests.Add(1)
	t.mu.Unlock()
	deregister := strings.HasPrefix(request.URL.Path, "/v1/agent/service/deregister/")
	heartbeat := strings.HasPrefix(request.URL.Path, "/v1/agent/check/update/")
	if !deregister && t.ctx.Err() != nil {
		t.requests.Done()
		if request.Body != nil {
			_ = request.Body.Close()
		}
		if heartbeat {
			return localResponse(request), nil
		}
		return nil, context.Canceled
	}
	ctx, cancel := context.WithCancel(request.Context())
	stop := func() bool { return false }
	if !deregister {
		stop = context.AfterFunc(t.ctx, cancel)
	}
	finish := func() { stop(); cancel(); t.requests.Done() }
	response, err := t.base.RoundTrip(request.Clone(ctx))
	if err != nil {
		finish()
		if heartbeat && t.ctx.Err() != nil {
			return localResponse(request), nil
		}
		return nil, errors.New("Consul request failed")
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		_ = response.Body.Close()
		body := "Consul request failed"
		response.Body = io.NopCloser(strings.NewReader(body))
		response.ContentLength = int64(len(body))
	}
	response.Body = &libraryBody{ReadCloser: response.Body, finish: finish}
	return response, nil
}

// close 停止新的外部请求，并等待已取消或受 HTTP 超时约束的心跳请求结束。
func (t *libraryTransport) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.requests.Wait()
}

// localResponse 仅供库内心跳收尾使用，不用于判断或记录远端操作成功。
func localResponse(request *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}
}

// libraryBody 在 SDK 完成读取或关闭响应后释放请求，避免提前取消响应或遗漏在途请求。
type libraryBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

// Close 关闭实际响应，并只完成一次请求清理。
func (b *libraryBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.finish)
	return err
}
