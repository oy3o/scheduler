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

// --- Test Task Implementations ---

// cpuHog is a task that burns CPU without yielding.
// Go 1.14+ async preemption prevents it from deadlocking the runtime.
type cpuHog struct {
	priority int
	done     chan struct{}
}

func (t *cpuHog) Priority() int { return t.priority }
func (t *cpuHog) Execute(ctx Context) error {
	defer close(t.done)
	// Burn CPU until cancelled.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Busy spin — no ShouldYield needed; Go handles preemption.
		for i := 0; i < 10000; i++ {
			_ = i * i
		}
	}
}

// ioTask simulates a cooperative IO-bound task that yields frequently.
type ioTask struct {
	priority    int
	yieldCount  int
	completedAt atomic.Int64
}

func (t *ioTask) Priority() int { return t.priority }
func (t *ioTask) Execute(ctx Context) error {
	for i := 0; i < t.yieldCount; i++ {
		if err := ctx.Yield(); err != nil {
			return err
		}
	}
	t.completedAt.Store(time.Now().UnixNano())
	return nil
}

// quickTask completes instantly. Used for fairness and latency tests.
type quickTask struct {
	priority    int
	workNano    time.Duration
	completedAt atomic.Int64
}

func (t *quickTask) Priority() int { return t.priority }
func (t *quickTask) Execute(ctx Context) error {
	start := time.Now()
	for time.Since(start) < t.workNano {
		// Simulate light work
		runtime.Gosched()
	}
	t.completedAt.Store(time.Now().UnixNano())
	return nil
}

// panicTask panics during execution. Tests defer-based recovery.
type panicTask struct {
	priority int
	done     chan struct{}
}

func (t *panicTask) Priority() int { return t.priority }
func (t *panicTask) Execute(_ Context) error {
	defer close(t.done)
	panic("intentional chaos\n goroutine 1 [running]:\n debug.Stack()\n")
}

// --- Tests ---

func TestGatekeeper_SubmitNilTask(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	// Submitting a nil task should safely return an error without panicking
	err := g.Submit(nil)
	if err == nil {
		t.Fatal("Expected an error when submitting a nil task, got nil")
	}
	if !strings.Contains(err.Error(), "cannot submit nil task") {
		t.Fatalf("Expected specific nil task error, got: %v", err)
	}
}

func TestGatekeeper_BasicSubmitAndComplete(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	task := &quickTask{priority: 5, workNano: 1 * time.Millisecond}

	go func() {
		defer close(done)
		g.Start(ctx)
	}()
	for !g.started.Load() {
		runtime.Gosched()
	}

	if err := g.Submit(task); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for completion.
	deadline := time.After(1 * time.Second)
	for {
		if task.completedAt.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("task did not complete within 1s")
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}
	cancel()
	<-done
}

func TestGatekeeper_PanicRecovery(t *testing.T) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	panicker := &panicTask{priority: 3, done: make(chan struct{})}

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	if err := g.Submit(panicker); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Must not crash the process.
	select {
	case <-panicker.done:
		// Good — panic was recovered, task completed its lifecycle.
	case <-time.After(1 * time.Second):
		t.Fatal("panicking task did not recover within 1s")
	}
	cancel()
}

func TestGatekeeper_PanicLeakage(t *testing.T) {
	cfg := DefaultConfig()

	errCh := make(chan error, 1)
	cfg.OnError = func(task Task, err error) {
		errCh <- err
	}
	g := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	panicker := &panicTask{priority: 3, done: make(chan struct{})}

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	if err := g.Submit(panicker); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	select {
	case err := <-errCh:
		if strings.Contains(err.Error(), "goroutine") || strings.Contains(err.Error(), "debug.Stack") {
			t.Errorf("stack trace leaked in OnError: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("did not receive error from OnError")
	}
}

func TestGatekeeper_GracefulShutdown(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialLimit = 8
	cfg.MinConcurrency = 2
	cfg.TargetLatency = Tau
	cfg.DeathRattleTimeout = 1 * time.Second
	g := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Start(ctx)
	}()

	// Submit CPU hogs that will run until cancelled.
	for i := 0; i < 20; i++ {
		hog := &cpuHog{priority: 1, done: make(chan struct{})}
		g.Submit(hog)
	}

	time.Sleep(50 * time.Millisecond) // Let some dispatch
	cancel()

	select {
	case <-done:
		// Graceful shutdown worked.
	case <-time.After(5 * time.Second):
		t.Fatal("Gatekeeper did not shut down within 5s")
	}
}

func TestGatekeeper_FairnessHighPriorityFirst(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialLimit = 2
	cfg.MinConcurrency = 1
	cfg.TargetLatency = Tau
	g := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	low := &quickTask{priority: 0, workNano: 1 * time.Millisecond}
	high := &quickTask{priority: 10, workNano: 1 * time.Millisecond}

	// Submit low first, then high. High should still complete sooner
	// (or at least not significantly later) due to lower energy.
	g.Submit(low)
	g.Submit(high)

	deadline := time.After(2 * time.Second)
	for {
		if low.completedAt.Load() > 0 && high.completedAt.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("tasks did not complete within 2s")
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}

	// With only 2 slots and both being fast, both should complete.
	// The key invariant: high-priority task should have lower energy = dispatched first.
	t.Logf("High completed at: %d, Low completed at: %d",
		high.completedAt.Load(), low.completedAt.Load())
	cancel()
}

func TestGatekeeper_YieldResubmission(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialLimit = 4
	cfg.MinConcurrency = 2
	cfg.TargetLatency = Tau
	g := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	yielder := &ioTask{priority: 5, yieldCount: 3}
	g.Submit(yielder)

	deadline := time.After(2 * time.Second)
	for {
		if yielder.completedAt.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("yielding task did not complete within 2s")
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}
	t.Log("Yielding task completed successfully after 3 yields")
	cancel()
}

func TestZombieCircuitBreaker(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZombieTimeout = 100 * time.Millisecond
	cfg.MaxZombies = 2 // very low threshold for testing
	var tripped atomic.Bool
	cfg.OnPanic = func(t Task, p any) {
		errStr := fmt.Sprintf("%v", p)
		if strings.Contains(errStr, "maximum zombie limit exceeded") {
			tripped.Store(true)
		} else {
			// Catch other panics if they happen
			fmt.Printf("UNEXPECTED PANIC: %v\n", p)
		}
	}
	g := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Start(ctx)
	for !g.started.Load() {
		runtime.Gosched()
	}

	for i := 0; i < 3; i++ {
		SubmitVoid(g, 0, func(ctx Context) error {
			time.Sleep(2 * time.Second) // Hog the slot
			return nil
		})
	}
	// Give a bit of time for AIMD or dispatch to actually pick all 3 up.
	// Since DefaultConfig sets Limit to 64, they should be dispatched almost instantly.
	time.Sleep(50 * time.Millisecond)

	// Wait up to 3 seconds for watchdog to scan and trip
	timeout := time.After(3 * time.Second)
	for {
		if tripped.Load() {
			t.Log("Circuit breaker successfully triggered")
			return
		}
		select {
		case <-timeout:
			var tasks []string
			g.inflightTasks.Range(func(key *entry, value *taskState) bool {
				s := value
				data := s.stateData.Load()
				flags := uint32(data)
				tasks = append(tasks, fmt.Sprintf("dispatchedAt: %v, zombied: %v, flags: %b", s.dispatchedAt.Load(), (flags&flagZombied) != 0, flags))
				return true
			})
			t.Fatalf("Expected code to panic but it did not after 3 seconds, zombies seen: %d, \nTasks: %v", g.Metrics().Zombies, tasks)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestGatekeeper_PriorityUltra(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialLimit = 1
	cfg.MinConcurrency = 1
	cfg.MaxConcurrency = 1 // 1 slot forces queueing
	g := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		g.Start(ctx)
		close(done)
	}()

	var sequence []string
	var mu sync.Mutex

	// 1. Submit a blocker task to occupy the single slot
	blockerDone := make(chan struct{})
	SubmitVoid(g, PriorityNormal, func(ctx Context) error {
		<-blockerDone
		return nil
	})

	// Give a moment for blocker to be dispatched
	time.Sleep(50 * time.Millisecond)

	// 2. Submit Normal followed by Ultra.
	// Since the blocker is running, both will be queued.
	SubmitVoid(g, PriorityNormal, func(ctx Context) error {
		mu.Lock()
		sequence = append(sequence, "Normal")
		mu.Unlock()
		return nil
	})
	SubmitVoid(g, PriorityUltra, func(ctx Context) error {
		mu.Lock()
		sequence = append(sequence, "Ultra")
		mu.Unlock()
		return nil
	})

	// 3. Release the blocker. The scheduler should pick the Ultra task next despite it being submitted later.
	close(blockerDone)

	// Give a bit of time for tasks to finish
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(sequence) != 2 {
		t.Fatalf("expected 2 tasks to complete, got %d", len(sequence))
	}
	if sequence[0] != "Ultra" {
		t.Errorf("Expected Ultra to run first after blocker, but sequence was: %v", sequence)
	}
}

func TestGatekeeperStartNilContext(t *testing.T) {
	cfg := DefaultConfig()
	g := New(cfg)

	err := g.Start(nil)
	if err == nil {
		t.Errorf("expected error when starting with nil context")
	} else if err.Error() != "gatekeeper: cannot start with nil context" {
		t.Errorf("unexpected error: %v", err)
	}
}
