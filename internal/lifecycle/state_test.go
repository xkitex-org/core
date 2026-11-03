package lifecycle

import (
	"errors"
	"sync"
	"testing"
)

// TestAvailabilityPolicy 验证无探针直接可用、有探针等待检查，以及暂时失败恢复。
func TestAvailabilityPolicy(t *testing.T) {
	direct := New(false)
	if direct.Available() {
		t.Fatal("starting instance available")
	}
	direct.Run()
	if !direct.Available() {
		t.Fatal("ungated instance waited for probes")
	}
	gated := New(true)
	gated.Run()
	if gated.Available() {
		t.Fatal("probe checks bypassed")
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
	state := New(true)
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
		t.Fatal("late probe revived a stopping instance")
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
