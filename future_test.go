package scheduler

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFuture_SubmitFuncAndVoid(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Test 1: Function with return value
	expectedStr := "oy3o_moonlight"
	f1, err := SubmitFunc(g, 10, func(c Context) (string, error) {
		return expectedStr, nil
	})
	if err != nil {
		t.Fatalf("SubmitFunc failed: %v", err)
	}

	res, err := f1.Get(context.Background())
	if err != nil {
		t.Fatalf("Future.Get returned unexpected error: %v", err)
	}
	if res != expectedStr {
		t.Errorf("Expected result %q, got %q", expectedStr, res)
	}

	// Test 2: Void function
	f2, err := SubmitVoid(g, 10, func(c Context) error {
		// simulate tiny workload
		time.Sleep(1 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("SubmitVoid failed: %v", err)
	}

	_, err = f2.Get(context.Background())
	if err != nil {
		t.Fatalf("Void Future.Get returned unexpected error: %v", err)
	}
}

func TestFuture_PanicIsolation(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Submit a task that intentionally panics
	f, err := SubmitFunc(g, 10, func(c Context) (int, error) {
		panic("bloody sincerity")
	})
	if err != nil {
		t.Fatalf("SubmitFunc failed: %v", err)
	}

	// The panic should be caught and returned as an error, NOT crash the test
	_, getErr := f.Get(context.Background())
	if getErr == nil {
		t.Fatal("Expected an error from panicked task, got nil")
	}
	if !strings.Contains(getErr.Error(), "bloody sincerity") {
		t.Errorf("Expected error to contain panic payload, got: %v", getErr)
	}
	// Verify that the stack trace is NOT leaked to the public API
	if strings.Contains(getErr.Error(), "goroutine") || strings.Contains(getErr.Error(), "debug.Stack") {
		t.Errorf("Expected public error to not contain stack trace, got: %v", getErr)
	}
}

func TestFuture_Join(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	f1, _ := SubmitFunc(g, 10, func(c Context) (int, error) { return 1, nil })
	f2, _ := SubmitFunc(g, 10, func(c Context) (int, error) { return 2, nil })
	f3, _ := SubmitFunc(g, 10, func(c Context) (int, error) { return 3, nil })

	results, err := Join(context.Background(), f1, f2, f3)
	if err != nil {
		t.Fatalf("Join returned unexpected error: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	if results[0] != 1 || results[1] != 2 || results[2] != 3 {
		t.Errorf("Unexpected results: %v", results)
	}
}

func TestFuture_JoinFailFast(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	customErr := errors.New("synthetic failure")

	f1, _ := SubmitFunc(g, 10, func(c Context) (int, error) {
		time.Sleep(10 * time.Millisecond)
		return 1, nil
	})
	f2, _ := SubmitFunc(g, 10, func(c Context) (int, error) {
		// This fails immediately
		return 0, customErr
	})

	// Join should fail fast when f2 completes, without waiting for f1
	_, err := Join(context.Background(), f1, f2)
	if !errors.Is(err, customErr) {
		t.Errorf("Expected Join to return %v, got %v", customErr, err)
	}
}

func TestFuture_ContextTimeoutAndEscape(t *testing.T) {
	// Restrict gatekeeper to 1 slot to force queuing
	g := New(Config{
		InitialLimit:   1,
		MinConcurrency: 1,
		MaxConcurrency: 1,
		TargetLatency:  Tau,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // will shutdown gatekeeper at the end

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// 1. Submit a task that hogs the only CPU slot
	hogStarted := make(chan struct{})
	SubmitVoid(g, 100, func(c Context) error {
		close(hogStarted)
		time.Sleep(500 * time.Millisecond)
		return nil
	})
	<-hogStarted

	// 2. Submit a task that gets stuck in the queue
	f, _ := SubmitVoid(g, 0, func(c Context) error {
		return nil
	})

	// 3. Try to Get the stuck task with a short timeout
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer timeoutCancel()

	start := time.Now()
	_, err := f.Get(timeoutCtx)
	elapsed := time.Since(start)

	// It should exit gracefully with DeadlineExceeded instead of deadlocking
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Expected DeadlineExceeded, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Get blocked for too long despite timeout: %v", elapsed)
	}
}

func TestFuture_GatekeeperShutdownEscape(t *testing.T) {
	// This test ensures that if Gatekeeper is killed and drops queued tasks,
	// Future.Get doesn't hang forever as long as the caller provided a Context.
	g := New(Config{InitialLimit: 1, MinConcurrency: 1, MaxConcurrency: 1})
	ctx, cancel := context.WithCancel(context.Background())

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Hog the slot
	SubmitVoid(g, 100, func(c Context) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})

	// Queue the victim task
	f, _ := SubmitVoid(g, 10, func(c Context) error {
		return nil
	})

	// Kill the gatekeeper while the victim is in the queue.
	// This will trigger drainHeap(), which discards the entry without calling Execute.
	cancel()

	// Use an escape hatch context
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer waitCancel()

	_, err := f.Get(waitCtx)
	if !errors.Is(err, ErrGateClosed) {
		t.Fatalf("Expected Get to escape via ErrGateClosed, got: %v", err)
	}
}
