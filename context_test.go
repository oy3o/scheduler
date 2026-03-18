package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestContext_NilState(t *testing.T) {
	var ctx Context // state is nil

	select {
	case <-ctx.Done():
		// Expected: closedChan returns immediately
	case <-time.After(time.Millisecond):
		t.Fatal("Expected Done() to return a closed channel for nil state")
	}

	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("Expected Err() to return context.Canceled, got: %v", err)
	}

	if val := ctx.Value("key"); val != nil {
		t.Errorf("Expected Value() to return nil, got: %v", val)
	}

	if _, ok := ctx.Deadline(); ok {
		t.Error("Expected Deadline() to return ok=false")
	}

	if err := ctx.Yield(); err == nil {
		t.Error("Expected Yield() to return error for nil state")
	}

	// Calling EnterSyscall or ExitSyscall on nil state should not panic
	ctx.EnterSyscall()
	if err := ctx.ExitSyscall(); err == nil {
		t.Error("Expected ExitSyscall() to return error for nil state")
	}
}

func TestContext_ExpiredVersion(t *testing.T) {
	// Create a dummy state with version 1
	state := &taskState{}
	// Set version to 1, flags to flagValid
	state.stateData.Store((1 << 32) | uint64(flagValid))

	// Context with version 2 (expired)
	ctx := Context{state: state, version: 2}

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(time.Millisecond):
		t.Fatal("Expected Done() to return a closed channel for expired version")
	}

	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("Expected Err() to return context.Canceled for expired version, got: %v", err)
	}

	if val := ctx.Value("key"); val != nil {
		t.Errorf("Expected Value() to return nil for expired version, got: %v", val)
	}

	if _, ok := ctx.Deadline(); ok {
		t.Error("Expected Deadline() to return ok=false for expired version")
	}

	if err := ctx.Yield(); err == nil {
		t.Error("Expected Yield() to return error for expired version")
	}

	ctx.EnterSyscall()
	if err := ctx.ExitSyscall(); err == nil {
		t.Error("Expected ExitSyscall() to return error for expired version")
	}
}

func TestContext_ValidForwarding(t *testing.T) {
	type key struct{}
	val := "value"

	baseCtx := context.WithValue(context.Background(), key{}, val)
	baseCtx, cancel := context.WithCancel(baseCtx)

	state := &taskState{}
	state.taskStateHot.Context = baseCtx
	// Set valid version and flag
	state.stateData.Store((1 << 32) | uint64(flagValid))

	ctx := Context{state: state, version: 1}

	if v := ctx.Value(key{}); v != val {
		t.Errorf("Expected Value() to forward properly, got: %v, want: %v", v, val)
	}

	// Cancel the underlying context
	cancel()

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(time.Millisecond):
		t.Fatal("Expected Done() to return the underlying cancelled channel")
	}

	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("Expected Err() to forward correctly, got: %v", err)
	}
}

func TestContext_Yield_InSyscall(t *testing.T) {
	state := &taskState{}
	// Set valid version, flagValid, AND flagInSyscall
	state.stateData.Store((1 << 32) | uint64(flagValid) | uint64(flagInSyscall))

	ctx := Context{state: state, version: 1}

	err := ctx.Yield()
	if err == nil || err.Error() != "cannot Yield() while in Syscall state" {
		t.Errorf("Expected 'cannot Yield() while in Syscall state', got: %v", err)
	}
}

func TestContext_ExitSyscall_ClearFlags(t *testing.T) {
	// A basic test to make sure Enter/Exit Syscall manipulate flags safely,
	// though they also interact heavily with gatekeeper logic.

	// Create minimal gatekeeper just to satisfy nil deref checks if they happen
	cfg := DefaultConfig()
	g := New(cfg)

	state := &taskState{}
	state.gate = g
	// Set up basic entry
	state.entry = &entry{}

	state.stateData.Store((1 << 32) | uint64(flagValid))
	state.start.Store(NowNano())

	ctx := Context{state: state, version: 1}

	ctx.EnterSyscall()

	// Check if in syscall
	data := state.stateData.Load()
	if uint32(data)&flagInSyscall == 0 {
		t.Error("Expected flagInSyscall to be set")
	}

	// Exiting syscall actually requires requeueAndWait, which interacts with queues.
	// We'll skip testing the full ExitSyscall here and leave it to integration tests
	// like TestGatekeeper_YieldResubmission which cover this holistically.
}
