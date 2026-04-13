package scheduler

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
)

// Void represents an empty value for tasks that don't return anything.
// It uses zero memory and makes generic signatures cleaner.
type Void struct{}

// Future is a lightweight handle for an asynchronous task's result.
//
// Philosophy (Bridging the Void): It bridges the strict, low-level Gatekeeper scheduling
// model with a developer-friendly closure API. It is the temporal crystallization of a
// promise—that a computed value (or an isolated failure) will eventually materialize,
// allowing linear-style coordination across highly concurrent, non-linear work boundaries.
type Future[T any] struct {
	val       T            // Holds the result value of type T
	err       atomic.Value // Holds the execution error, if any
	done      chan struct{}
	closeOnce sync.Once
	aborter   func() // Link to closureTask.Abort for fail-fast cleanup in Join
}

func (f *Future[T]) closeDone() {
	f.closeOnce.Do(func() { close(f.done) })
}

// Get blocks until the task completes or the provided context is cancelled.
//
// WARNING (Zombie Leak Contract):
// Cancelling the context passed to Get() ONLY unblocks this call.
// It DOES NOT cancel the execution of the task within the Gatekeeper.
// The underlying closureTask will continue running and consuming a slot
// until it completes or the Gatekeeper itself shuts down.
//
// To implement true cooperative cancellation, your task closure must
// explicitly check ctx.Err() or use select on ctx.Done() internally.
//
// If the Gatekeeper shuts down while this task is still queued, the task
// will be dropped without execution. Always pass a context with a timeout
// as an escape hatch to prevent goroutine leaks in the caller.
func (f *Future[T]) Get(ctx context.Context) (T, error) {
	if ctx == nil {
		var zero T
		return zero, fmt.Errorf("gatekeeper: cannot use nil context in Future.Get")
	}
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-f.done:
		rawErr := f.err.Load()
		if rawErr != nil {
			var zero T
			return zero, rawErr.(error)
		}
		return f.val, nil
	}
}

// closureTask is the internal bridge that implements the Task interface
// while holding the user's generic closure and future pointer.
type closureTask[T any] struct {
	priority int
	fn       func(ctx Context) (T, error)
	future   *Future[T]
}

func (c *closureTask[T]) Priority() int {
	return c.priority
}

func (c *closureTask[T]) Abort() {
	c.future.err.CompareAndSwap(nil, ErrGateClosed)
	c.future.closeDone()
}

// Execute is called by the Gatekeeper. It injects panic recovery to ensure
// the Future always resolves, even if the user's logic explodes.
func (c *closureTask[T]) Execute(ctx Context) (err error) {
	// Guarantee the future is unblocked upon exit.
	defer func() {
		c.future.closeDone()
	}()

	// Panic isolation boundary (Bloody Sincerity):
	// We do not silently swallow panics, nor do we let a single reckless user task
	// crash the critical infrastructure of the gatekeeper host. We catch it,
	// wrap it as a hard error containing the stack trace, and return it.
	// Gatekeeper's dispatch loop will observe this and forcefully route
	// it to the system-level Config.OnError hook for downstream alerting.
	defer func() {
		if p := recover(); p != nil {
			internalErr := fmt.Errorf("task panicked: %v\n%s", p, debug.Stack())

			// 🛡️ Sentinel: Sanitize the public error to prevent stack trace leakage.
			// If the user's panic payload itself contains multiple lines (e.g. they
			// re-panicked an error with a stack trace), we strip everything after
			// the first line to keep the Future API clean.
			// 🛡️ Sentinel: Prevent stack trace leakage via alternate whitespace formats.
			pStr := fmt.Sprintf("%v", p)
			if idx := strings.IndexAny(pStr, "\n\r\f\v"); idx != -1 {
				pStr = pStr[:idx]
			}
			publicErr := fmt.Errorf("task panicked: %s", pStr)

			c.future.err.CompareAndSwap(nil, publicErr)
			err = internalErr // Let the Gatekeeper bleed. Do not swallow.
		}
	}()

	res, resErr := c.fn(ctx)
	if resErr != nil {
		c.future.err.CompareAndSwap(nil, resErr)
	} else {
		c.future.val = res
	}
	return nil
}

// SubmitFunc wraps a generic function into a Task and submits it to the Gatekeeper.
// It returns a Future that can be awaited, eliminating the need to define custom Task structs.
//
// WARNING (Zombie Leak Contract):
// The Context passed to Future.Get() only controls the caller's blocking.
// It does NOT propagate cancellation into the submitted closure.
// Your fn must cooperatively check the Gatekeeper's Context (the ctx argument)
// for cancellation. See Future.Get documentation for details.
func SubmitFunc[T any](g *Gatekeeper, priority int, fn func(ctx Context) (T, error)) (*Future[T], error) {
	if fn == nil {
		return nil, fmt.Errorf("gatekeeper: cannot submit nil function")
	}

	f := &Future[T]{
		done: make(chan struct{}),
	}
	task := &closureTask[T]{
		priority: priority,
		fn:       fn,
		future:   f,
	}

	f.aborter = task.Abort
	if err := g.Submit(task); err != nil {
		return nil, err
	}
	return f, nil
}

// SubmitVoid is a syntactic sugar for submitting tasks that do not return a value.
func SubmitVoid(g *Gatekeeper, priority int, fn func(ctx Context) error) (*Future[Void], error) {
	if fn == nil {
		return nil, fmt.Errorf("gatekeeper: cannot submit nil function")
	}
	return SubmitFunc(g, priority, func(ctx Context) (Void, error) {
		return Void{}, fn(ctx)
	})
}

// Join acts as a concurrency synchronization barrier (The Architect's Convergence).
// It waits for a variadic slice of Futures to complete and returns their collective results.
//
// Philosophy (Fail Fast): If ANY Future in the set fails or panics, Join instantly aborts
// and bubbles the error up. It refuses to wait for the rest if the overarching premise
// is already compromised. Structural integrity precedes completion.
func Join[T any](ctx context.Context, futures ...*Future[T]) ([]T, error) {
	if ctx == nil {
		return nil, fmt.Errorf("gatekeeper: cannot use nil context in Join")
	}
	if len(futures) == 0 {
		return nil, nil
	}

	// [Phase 1: Non-Blocking Fast Fail Pre-Check & Validation]
	// Spin through all futures instantly without blocking. If any task has ALREADY
	// panicked or returned an error, we catch it in O(N) nanoseconds and abort
	// the entire batch before settling into the blocking wait.
	// We also perform nil-checks here to merge redundant iterations.
	for _, f := range futures {
		if f == nil {
			return nil, fmt.Errorf("gatekeeper: cannot join nil future")
		}
		select {
		case <-f.done:
			if err := f.err.Load(); err != nil {
				// Fail Fast cleanup: Abort all other tasks in the join set
				for _, fut := range futures {
					if fut != nil && fut.aborter != nil {
						fut.aborter()
					}
				}
				return nil, err.(error)
			}
		default:
		}
	}

	// Create an internal cancellation boundary.
	// This ensures that if Join fails fast, we automatically unblock
	// any nested or chained tasks relying on this specific join context.
	joinCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]T, len(futures))

	// [Phase 2: Serial Polling]
	// We iterate through futures and block on them sequentially. By sacrificing
	// perfect chronological failure detection (a crash in future[10] won't be seen
	// until future[0..9] resolve), we completely eliminate the massive overhead of
	// spawning N waiter goroutines. This is a deliberate "Physical Symbiosis" tradeoff.
	for i, f := range futures {
		val, err := f.Get(joinCtx)
		if err != nil {
			// Fail Fast cleanup: Abort all other tasks in the join set
			for _, fut := range futures {
				if fut.aborter != nil {
					fut.aborter()
				}
			}
			return nil, err
		}
		results[i] = val
	}

	return results, nil
}
