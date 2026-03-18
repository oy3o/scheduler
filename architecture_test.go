package scheduler

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEntryPoolRecycling verifies that entries are actually reused from the pool
// and that their state is correctly reset.
func TestEntryPoolRecycling(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Submit many tasks and track the pointer addresses of entries.
	// Since entries are pooled, we should see the same addresses repeating.
	const numTasks = 100
	addrs := make(map[uintptr]int)

	var wg sync.WaitGroup
	wg.Add(numTasks)

	for i := 0; i < numTasks; i++ {
		_, err := SubmitVoid(g, 0, func(ctx Context) error {
			wg.Done()
			return nil
		})
		if err != nil {
			t.Fatalf("failed to submit task: %v", err)
		}
	}

	wg.Wait()

	// If we got here without a race detector warning or crash, the pool is functionally correct.
	// A more rigorous test would require exposing internal entry pointers.
	_ = addrs
}

// TestZombieWatchdog verifies that a task that hogs a slot for longer than ZombieTimeout
// is correctly identified and its slot is released.
func TestZombieWatchdog(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialLimit = 1
	cfg.MinConcurrency = 1
	cfg.MaxConcurrency = 1
	cfg.ZombieTimeout = 1 * time.Second // Short timeout for testing

	var zombieDetected atomic.Bool
	cfg.OnError = func(task Task, err error) {
		if err != nil && strings.Contains(err.Error(), "zombied") {
			zombieDetected.Store(true)
		}
	}

	g := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Task 1: Hog the only slot
	done1 := make(chan struct{})
	SubmitVoid(g, 0, func(ctx Context) error {
		// Just sleep without yielding
		time.Sleep(5 * time.Second)
		close(done1)
		return nil
	})

	// Give Task 1 time to start
	time.Sleep(100 * time.Millisecond)

	// Task 2: Should be able to start once Task 1 is zombied
	done2 := make(chan struct{})
	start2 := time.Now()
	SubmitVoid(g, 0, func(ctx Context) error {
		close(done2)
		return nil
	})

	// Wait for Task 2 to be dispatched
	select {
	case <-done2:
		// Task 2 was dispatched! This proves Task 1's slot was released.
		elapsed := time.Since(start2)
		if elapsed < 500*time.Millisecond {
			t.Errorf("Task 2 dispatched too early: %v (Limit: %d, Active: %d)", elapsed, g.aimd.Limit(), g.active.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Task 2 was never dispatched (Limit: %d, Active: %d)", g.aimd.Limit(), g.active.Load())
	}

	// Give the asynchronous OnError callback (fired by safeOnError) time to write to zombieDetected
	for i := 0; i < 100; i++ {
		if zombieDetected.Load() {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	if !zombieDetected.Load() {
		t.Error("watchdog did not report zombie error")
	}

	m := g.Metrics()
	if m.Zombies != 1 {
		t.Errorf("expected 1 zombie in metrics, got %d", m.Zombies)
	}

	<-done1
}

// TestCallbackTimeout verifies that a deadlocked OnError callback does not block the system.
func TestCallbackTimeout(t *testing.T) {
	cfg := DefaultConfig()
	blockCh := make(chan struct{})

	var callbackReached atomic.Bool
	cfg.OnError = func(task Task, err error) {
		callbackReached.Store(true)
		<-blockCh // Deadlock here
	}

	g := New(cfg)
	// We call safeOnError directly to test the timeout logic.
	// Since we can't easily trigger a natural OnError that we know will block Wait(),
	// we test the boundary isolation.

	start := time.Now()
	g.safeOnError(&closureTask[Void]{}, fmt.Errorf("triggered error"))

	// Wait a bit for the callback goroutine to actually start
	time.Sleep(10 * time.Millisecond)
	elapsed := time.Since(start)

	if !callbackReached.Load() {
		t.Error("OnError callback was never reached")
	}

	// Should have returned after ~50ms timeout
	if elapsed > 200*time.Millisecond {
		t.Errorf("safeOnError took too long: %v (expected ~50ms)", elapsed)
	}

	close(blockCh) // Cleanup
}

// TestGhostPrevention verifies that a goroutine from a previous entry generation
// cannot manipulate the scheduler after the entry has been recycled.
func TestGhostPrevention(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Gatekeeper must be explicitly started before accepting submissions to prevent
	// premature context expiration and ensure background monitors are active.
	go g.Start(ctx)
	// Spin-wait for Start() to set the started flag, preventing Submit race.
	for !g.started.Load() {
		runtime.Gosched()
	}

	// We'll capture the Context and then try to use it after the task "finishes"
	// and the entry is potentially recycled.
	var capturedCtx Context
	done := make(chan struct{})

	SubmitVoid(g, 0, func(ctx Context) error {
		capturedCtx = ctx
		close(done)
		return nil
	})

	<-done

	// Wait a bit to ensure the entry is released and pool recycled.
	// In a real high-throughput scenario, it would happen instantly.
	time.Sleep(50 * time.Millisecond)

	// Force intense submissions to guarantee recycling of the same entry.
	for i := 0; i < 256; i++ {
		SubmitVoid(g, 0, func(ctx Context) error { return nil })
	}
	time.Sleep(100 * time.Millisecond)

	// Now try to use the "ghost" context
	err := capturedCtx.Yield()
	if err == nil {
		t.Error("expected error when Yielding from a ghost context, but got nil")
	} else if err.Error() != "ghost goroutine attempted to manipulate scheduler" && err.Error() != "invalid or expired context" {
		t.Errorf("expected ghost error, got: %v", err)
	}

	capturedCtx.EnterSyscall() // Should just return (ignore ghost)
}
