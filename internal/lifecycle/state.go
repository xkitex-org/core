package lifecycle

import (
	"sync"

	"github.com/cloudwego/kitex/pkg/klog"
)

// Phase 表示单个 Server 的生命周期，不等同于是否允许发现和分配流量。
type Phase string

const (
	Starting Phase = "Starting"
	Running  Phase = "Running"
	Stopping Phase = "Stopping"
	Stopped  Phase = "Stopped"
	Failed   Phase = "Failed"
)

// State 保存在单个 Server 内，终止状态优先于异步检查结果。
type State struct {
	mu      sync.RWMutex
	phase   Phase
	service string
	gated   bool
	started bool
	ready   bool
	changed chan struct{}
}

// New 创建初始不可用状态；未接探针的实例在框架运行通知后直接允许使用。
func New(service string, gated bool) *State {
	if service == "" {
		service = "未确定"
	}
	s := &State{service: service, gated: gated, changed: make(chan struct{}, 1)}
	s.changePhase(Starting)
	return s
}

// changePhase 在持锁或构造期间按顺序记录实际状态变化，不重复输出相同阶段。
func (s *State) changePhase(next Phase) {
	if s.phase == next {
		return
	}
	previous := string(s.phase)
	if previous == "" {
		previous = "None"
	}
	s.phase = next
	s.notify()
	klog.Infof("core: 实例生命周期变化：service=[%s] from=[%s] to=[%s]", s.service, previous, next)
}

// Phase 返回只读阶段，供独立探针模块读取。
func (s *State) Phase() string { s.mu.RLock(); defer s.mu.RUnlock(); return string(s.phase) }

// Available 计算当前可用性，未接探针时不引入任何探针检查或等待。
func (s *State) Available() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.available() }

// available 在持锁期间计算可用性，不对外提供修改入口。
func (s *State) available() bool { return s.phase == Running && (!s.gated || s.started && s.ready) }

// Run 仅允许从启动阶段进入运行阶段，防止迟到的启动通知覆盖停机状态。
func (s *State) Run() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == Starting {
		s.changePhase(Running)
	}
}

// Stop 进入不可恢复的停机阶段，已终止或失败的状态保持不变。
func (s *State) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == Starting || s.phase == Running {
		s.changePhase(Stopping)
		s.ready = false
	}
}

// Finish 记录最终结果；已失败的实例不会被后续正常清理改回已停止。
func (s *State) Finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = false
	if err != nil || s.phase == Failed {
		s.changePhase(Failed)
	} else {
		s.changePhase(Stopped)
	}
}

// Report 接收检查结果，启动完成单向锁存，终止后不再接受后台检查恢复就绪。
func (s *State) Report(started, ready bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gated || s.phase != Running {
		return false
	}
	before := s.available()
	s.started = s.started || started
	s.ready = s.started && ready
	changed := before != s.available()
	if changed {
		s.notify()
	}
	return changed
}

// Changes 合并实例阶段或就绪变化，消费者读取最新状态而非回放过期检查结果。
func (s *State) Changes() <-chan struct{} { return s.changed }

// ProbeEnabled 返回该实例是否显式接入探针，此声明在构造后不再变化。
func (s *State) ProbeEnabled() bool { return s.gated }

// notify 在持锁或构造期间唤醒单个注册同步器，不阻塞健康检查和停机流程。
func (s *State) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
