package startup

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudwego/kitex/server"
	"go.chaoxizhongzhi.com/xkitex/core/internal/config"
	"go.chaoxizhongzhi.com/xkitex/core/internal/lifecycle"
	"go.chaoxizhongzhi.com/xkitex/core/internal/registration"
)

// serverFactory 为每个 Server 注入私有生命周期及退出通道，不使用全局启动或停机钩子。
type serverFactory func(context.Context, config.Settings, *lifecycle.State, <-chan error) server.Server

// managedServer 在 Kitex 监听后登记当前实例，沿用每实例退出信号及原生优雅排空。
func managedServer(ctx context.Context, settings config.Settings, state *lifecycle.State, exit <-chan error) server.Server {
	return newServer(settings, server.WithRegistry(registration.New(ctx, settings, state)), server.WithExitSignal(func() <-chan error { return exit }))
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

// reportProbe 保存实例的就绪依据；各探针状态由 probe 分别记录，不再仅输出汇总可用性。
func reportProbe(state *lifecycle.State, started, ready bool) {
	state.Report(started, ready)
}
