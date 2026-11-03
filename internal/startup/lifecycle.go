package startup

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/cloudwego/kitex/server"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
)

// lifecycleObserver 观察 Kitex 按实例提供的框架回调，不访问任何服务注册中心。
type lifecycleObserver struct{ state *lifecycle.State }

// Register 在 Kitex 自身的监听和启动流程之后记录运行阶段，不做外部注册或健康等待。
func (o lifecycleObserver) Register(*registry.Info) error { o.state.Run(); return nil }

// Deregister 在 RPC 排空前记录停机阶段，实际服务注销留待后续接入。
func (o lifecycleObserver) Deregister(*registry.Info) error { o.state.Stop(); return nil }

// serverFactory 为每个 Server 注入私有生命周期及退出通道，不使用全局启动或停机钩子。
type serverFactory func(config.Settings, *lifecycle.State, <-chan error) server.Server

// managedServer 沿用 Kitex 的阻塞运行和优雅排空，只对接按实例的运行通知及退出信号。
func managedServer(settings config.Settings, state *lifecycle.State, exit <-chan error) server.Server {
	return newServer(settings, server.WithRegistry(lifecycleObserver{state}), server.WithExitSignal(func() <-chan error { return exit }))
}

// supervise 在转发退出信号前撤销可用状态；探针自身故障进入失败状态，健康检查失败不会退出服务。
func supervise(parent context.Context, state *lifecycle.State) (context.Context, <-chan error, func(<-chan error), func()) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	exit := make(chan error, 1)
	done := make(chan struct{})
	start := func(fatal <-chan error) {
		go func() {
			var err error
			select {
			case <-signals:
			case <-parent.Done():
			case err = <-fatal:
				if err == nil {
					err = fmt.Errorf("core: probe service stopped unexpectedly")
				}
				state.Finish(err)
			case <-done:
				return
			}
			state.Stop()
			cancel()
			exit <- err
		}()
	}
	cleanup := func() { signal.Stop(signals); close(done); cancel() }
	return ctx, exit, start, cleanup
}

// reportProbe 汇总每个实例的可用性变化，只在变化时记录日志，避免定时检查刷屏。
func reportProbe(state *lifecycle.State, started, ready bool) {
	if state.Report(started, ready) {
		klog.Infof("core: 服务可用性变化：available=[%t]", state.Available())
	}
}
