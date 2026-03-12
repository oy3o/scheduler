package scheduler

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// closedChan is a universal closed channel used to terminate ghost goroutines
// immediately when they query an expired context, preventing them from blocking
// permanently on a nil channel.
var closedChan = make(chan struct{})

func init() {
	close(closedChan)
}

// taskState is the internal, pooled metadata for a task's current execution.
//
// Philosophy (Memory Geometry & Zero Allocations): We aggressively pool these states
// to drop GC pressure to zero on the hot path. However, pooling introduces the
// "Phantom Menace" (ABA problems). We guard against this using spatial partitioning
// (cache-line padding) to prevent false sharing, and temporal partitioning
// (valenced versions) to ensure ghost goroutines cannot hijack recycled memory.
type taskStateHot struct {
	context.Context
	task         Task
	start        atomic.Int64 // NowNano()
	dispatchedAt atomic.Int64 // NowNano()
	entry           *entry
	gate            *Gatekeeper
	stateData       atomic.Uint64 // high 32: version, low 32: flags
	watchdogPenalty atomic.Int64  // Added for lock-free penalty injection (Safe against concurrent recycle)
}

type taskState struct {
	taskStateHot
	_ [(cacheLineSize - (unsafe.Sizeof(taskStateHot{}) % cacheLineSize)) % cacheLineSize]byte
}

var _ = [1]struct{}{}[unsafe.Sizeof(taskState{}) % cacheLineSize]

const (
	flagValid uint32 = 1 << iota
	flagInSyscall
	flagZombied
	flagExiting // Added to prevent Watchdog race during dispatch defer termination
)

var taskStatePool = sync.Pool{
	New: func() any {
		return &taskState{}
	},
}

// acquireCtx grabs a recycled taskState and securely initializes it, incrementing
// its execution version so old Context references become permanently invalid.
func acquireCtx(ctx context.Context, g *Gatekeeper, e *entry) Context {
	s := taskStatePool.Get().(*taskState)
	s.taskStateHot.Context = ctx
	s.task = e.task
	s.gate = g
	s.entry = e
	s.start.Store(NowNano())
	s.dispatchedAt.Store(s.start.Load())

	var newVersion uint32
	for {
		oldData := s.stateData.Load()
		newVersion = uint32(oldData>>32) + 1
		newData := (uint64(newVersion) << 32) | uint64(flagValid)
		if s.stateData.CompareAndSwap(oldData, newData) {
			break
		}
	}

	return Context{state: s, version: newVersion}
}

func releaseCtx(s *taskState) {
	s.clearValid()
	// [Architectural Note]: We NO LONGER explicitly set Context, task, and entry to nil.
	// Eager partial-clearing of interfaces in a lock-free pool creates a fatal TOCTOU race
	// where a ghost goroutine passes the version check but panics upon dereferencing
	// the cleared interface pointer after a preemption. We rely entirely on the 
	// Valenced Versioning (flagValid) boundary to reject ghost interactions, and accept 
	// the delayed GC of the context tree as the necessary cost of zero-lock memory safety.
	s.gate = nil
	s.watchdogPenalty.Store(0)
	taskStatePool.Put(s)
}

func (s *taskState) setZombied(version uint32) bool {
	for {
		data := s.stateData.Load()
		if uint32(data>>32) != version {
			return false
		}
		// If the task is already exiting or in a syscall, the Watchdog must back off 
		// to prevent double-release of the gate slot or incorrect zombie marking.
		if uint32(data)&(flagZombied|flagExiting|flagInSyscall) != 0 {
			return false
		}
		if s.stateData.CompareAndSwap(data, data|uint64(flagZombied)) {
			return true
		}
	}
}

func (s *taskState) clearZombied(version uint32) bool {
	for {
		data := s.stateData.Load()
		if uint32(data>>32) != version {
			return false
		}
		if uint32(data)&flagZombied == 0 {
			return false
		}
		if s.stateData.CompareAndSwap(data, data&^uint64(flagZombied)) {
			return true
		}
	}
}

func (s *taskState) setInSyscall(version uint32) bool {
	for {
		data := s.stateData.Load()
		if uint32(data>>32) != version {
			return false
		}
		if uint32(data)&flagInSyscall != 0 {
			return false
		}
		if s.stateData.CompareAndSwap(data, data|uint64(flagInSyscall)) {
			return true
		}
	}
}

func (s *taskState) clearInSyscall(version uint32) bool {
	for {
		data := s.stateData.Load()
		if uint32(data>>32) != version {
			return false
		}
		if uint32(data)&flagInSyscall == 0 {
			return false
		}
		if s.stateData.CompareAndSwap(data, data&^uint64(flagInSyscall)) {
			return true
		}
	}
}

func (s *taskState) clearValid() {
	for {
		data := s.stateData.Load()
		if uint32(data)&flagValid == 0 {
			return
		}
		if s.stateData.CompareAndSwap(data, data&^uint64(flagValid)) {
			return
		}
	}
}

// Done returns the channel that is closed when the Gatekeeper shuts down.
func (c Context) Done() <-chan struct{} {
	if c.state == nil {
		return closedChan
	}
	data := c.state.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return closedChan
	}
	return c.state.taskStateHot.Done()
}

// Err returns the context error.
func (c Context) Err() error {
	if c.state == nil {
		return context.Canceled
	}
	data := c.state.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return context.Canceled
	}
	return c.state.taskStateHot.Err()
}

// Value implements the context.Context interface.
func (c Context) Value(key any) any {
	if c.state == nil {
		return nil
	}
	data := c.state.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return nil
	}
	return c.state.taskStateHot.Value(key)
}

// Deadline implements the context.Context interface.
func (c Context) Deadline() (deadline time.Time, ok bool) {
	if c.state == nil {
		return time.Time{}, false
	}
	data := c.state.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return time.Time{}, false
	}
	return c.state.taskStateHot.Deadline()
}

// Yield voluntarily suspends this task and re-enqueues it with updated energy.
const minYieldLatencyNano = int64(time.Microsecond) * 100

func (c Context) Yield() error {
	s := c.state
	if s == nil {
		return errors.New("invalid or expired context")
	}

	data := s.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return errors.New("ghost goroutine attempted to manipulate scheduler")
	}

	flags := uint32(data)
	if flags&flagInSyscall != 0 {
		return errors.New("cannot Yield() while in Syscall state")
	}

	if s.gate.closed.Load() {
		return ErrGateClosed
	}

	now := NowNano()
	sliceNano := now - s.start.Load()

	// Record latency before yielding!
	if sliceNano > 0 {
		s.gate.recordLatency(time.Duration(sliceNano))
	}

	// Harvest penalties from the stable state container
	statePenalty := s.watchdogPenalty.Swap(0)
	s.entry.runtimeNano += sliceNano + statePenalty
	s.entry.yieldCount++

	if flags&flagZombied == 0 {
		s.gate.releaseSlot()
	} else {
		// Only decrement zombieCount if we successfully cleared the flag.
		// Prevents underflow if multiple goroutines race or Watchdog re-triggers.
		if s.clearZombied(c.version) {
			s.gate.zombieCount.Add(-1)
		}
	}

	if sliceNano < minYieldLatencyNano {
		runtime.Gosched()
	}

	return c.requeueAndWait()
}

func (c Context) requeueAndWait() error {
	s := c.state
	w := Weight(s.entry.task.Priority())
	s.entry.creationPressure = float64(s.gate.recentCreations.Load())
	s.entry.energy = Energy(
		float64(s.entry.runtimeNano),
		w,
		s.entry.creationPressure,
		s.entry.yieldCount,
	)

	s.entry.state.Store(stateQueued)
	if s.entry.task.Priority() >= PriorityUltra {
		s.gate.fastQueue.Push(s.entry)
	} else {
		s.gate.heap.push(s.entry)
	}
	s.gate.queued.Add(1)

	if s.gate.closed.Load() {
		if s.entry.state.CompareAndSwap(stateQueued, stateDead) {
			s.gate.queued.Add(-1)
			select {
			case s.entry.wake <- struct{}{}:
			default:
			}
			// [Architectural Note]: Slot Compensation Protocol.
			// When the gate is closed, we must manually increment 'active' by 1.
			// This is because the caller (dispatch loop) WILL eventually call
			// releaseSlot() via its defer chain, which decrements 'active'.
			// By providing this +1 here, we neutralize the pending release
			// and ensure the global slot count remains invariants-compliant.
			s.gate.active.Add(1)
			return ErrGateClosed
		}
	}

	s.gate.signal()

	select {
	case <-s.entry.wake:
		if s.entry.state.Load() == stateDead {
			return c.resolveYieldCancel()
		}
		now := NowNano()
		s.start.Store(now)
		// Reset dispatch time to prevent Watchdog from assassinating
		// long-running but fully cooperative tasks.
		s.dispatchedAt.Store(now)
		return nil
	case <-s.Done():
		return c.resolveYieldCancel()
	}
}

func (c Context) resolveYieldCancel() error {
	s := c.state
	if s.entry.state.CompareAndSwap(stateQueued, stateDead) {
		s.gate.queued.Add(-1)
		s.gate.active.Add(1)
	} else {
		currentState := s.entry.state.Load()
		if currentState == stateDispatched {
			// [Architectural Note]: We do NOT call s.gate.active.Add(1) here by default.
			// The scheduler loop has already incremented 'active' when it popped the entry
			// and transitioned it to stateDispatched. By waiting for the wake signal,
			// we acknowledge the transfer of ownership to the local goroutine.
			//
			// [Deadlock Prevention]: We must NOT block indefinitely. If the root
			// context is cancelled or the Gatekeeper stops, we MUST abort to
			// allow for graceful termination of the goroutine.
			select {
			case <-s.entry.wake:
				// [Architectural Note]: The dispatch loop's defer will handle releaseSlot().
			case <-s.gate.rootCtxDone():
			case <-s.gate.doneCh:
				// If rootCtxDone is nil, we must still respect Gatekeeper shutdown
			}
		} else {
			// If CAS failed and state is NOT dispatched (e.g. stateQueued/stateDead),
			// we must compensate for the pending releaseSlot in the caller's defer.
			s.gate.active.Add(1)
		}
	}
	if s.gate.closed.Load() {
		return ErrGateClosed
	}
	return s.Err()
}

func (c Context) EnterSyscall() {
	s := c.state
	if s == nil {
		return
	}

	data := s.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return
	}

	if s.setInSyscall(c.version) {
		now := NowNano()
		sliceNano := now - s.start.Load()

		// Record latency before hiding in syscall!
		if sliceNano > 0 {
			s.gate.recordLatency(time.Duration(sliceNano))
		}

		s.entry.runtimeNano += sliceNano
		s.start.Store(now)

		// [Architectural Note]: setInSyscall() acts as a memory barrier. After it succeeds,
		// setZombied() will fail because flagInSyscall is set. Therefore, re-reading stateData
		// below cannot observe a newly-set flagZombied from a concurrent Watchdog tick.
		currentData := s.stateData.Load()
		if uint32(currentData)&flagZombied == 0 {
			s.gate.releaseSlot()
		} else {
			s.clearZombied(c.version)
			s.gate.zombieCount.Add(-1)
		}
		s.gate.signal()
	}
}

func (c Context) ExitSyscall() error {
	s := c.state
	if s == nil {
		return errors.New("invalid or expired context")
	}

	data := s.stateData.Load()
	if uint32(data>>32) != c.version || uint32(data)&flagValid == 0 {
		return errors.New("ghost goroutine attempted to manipulate scheduler")
	}

	if !s.clearInSyscall(c.version) {
		return nil
	}

	if s.gate.closed.Load() {
		s.gate.active.Add(1)
		return ErrGateClosed
	}

	s.entry.yieldCount++

	// Harvest penalties accumulated during syscall
	statePenalty := s.watchdogPenalty.Swap(0)
	s.entry.runtimeNano += statePenalty

	return c.requeueAndWait()
}

// Use a simple guard for now to bypass mathematical precision issues with unsafe.Sizeof
var _ = [1]struct{}{}[unsafe.Sizeof(entry{})%8]
var _ = [1]struct{}{}[unsafe.Sizeof(taskState{})%8]
