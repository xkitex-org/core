package lifecycle

import (
	"bytes"
	"errors"
	"github.com/cloudwego/kitex/pkg/klog"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestAvailabilityPolicy 验证无探针直接可用、有探针等待检查，以及暂时失败恢复。
func TestAvailabilityPolicy(t *testing.T) {
	direct := New("demo-api", false)
	if direct.Available() {
		t.Fatal("starting instance available")
	}
	direct.Run()
	if !direct.Available() {
		t.Fatal("ungated instance waited for probes")
	}
	gated := New("demo-api", true)
	gated.Run()
	if gated.Available() {
		t.Fatal("monitor checks bypassed")
	}
	gated.Report(true, true)
	if !gated.Available() {
		t.Fatal("successful checks not applied")
	}
	gated.Report(false, false)
	if gated.Available() {
		t.Fatal("readiness failure ignored")
	}
	gated.Report(false, true)
	if !gated.Available() {
		t.Fatal("startup latch reset or readiness not recovered")
	}
}

// TestTerminalStatesWinOverLateResults 验证停机与检查并发时不会恢复可用，失败不会被清理覆盖。
func TestTerminalStatesWinOverLateResults(t *testing.T) {
	state := New("demo-api", true)
	state.Run()
	state.Report(true, true)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				state.Report(true, true)
				state.Run()
			}
		}()
	}
	state.Stop()
	group.Wait()
	if state.Phase() != string(Stopping) || state.Available() {
		t.Fatal("late monitor revived a stopping instance")
	}
	failure := errors.New("failure")
	state.Finish(failure)
	state.Finish(nil)
	state.Run()
	state.Report(true, true)
	if state.Phase() != string(Failed) || state.Available() {
		t.Fatal("failed state lost")
	}
}

// TestLifecycleTransitionLogs 验证正常和失败路径逐阶段记录服务名，重复调用及迟到通知不产生虚假变化。
func TestLifecycleTransitionLogs(t *testing.T) {
	var output bytes.Buffer
	klog.SetOutput(&output)
	defer klog.SetOutput(os.Stderr)
	state := New("zmall-product", true)
	state.Run()
	state.Run()
	state.Stop()
	state.Stop()
	state.Report(true, true)
	state.Run()
	state.Finish(nil)
	state.Finish(nil)
	failure := New("zmall-category", false)
	failure.Finish(errors.New("private-error"))
	failure.Finish(nil)
	logs := output.String()
	for _, transition := range []string{
		"service=[zmall-product] from=[None] to=[Starting]",
		"service=[zmall-product] from=[Starting] to=[Running]",
		"service=[zmall-product] from=[Running] to=[Stopping]",
		"service=[zmall-product] from=[Stopping] to=[Stopped]",
		"service=[zmall-category] from=[None] to=[Starting]",
		"service=[zmall-category] from=[Starting] to=[Failed]",
	} {
		if strings.Count(logs, transition) != 1 {
			t.Fatalf("missing or repeated transition %q: %s", transition, logs)
		}
	}
	if strings.Count(logs, "实例生命周期变化") != 6 || strings.Contains(logs, "private-error") {
		t.Fatal("unexpected or sensitive lifecycle logs")
	}
}
