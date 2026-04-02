package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var epoch = time.Now()

// NowNano returns strictly monotonic nanoseconds since epoch.
func NowNano() int64 {
	return int64(time.Since(epoch))
}

const (
	maxLatencySamples = 4096
	numShards         = 8
	cacheLineSize     = 128
)

// Gatekeeper is the energy-based admission controller.
type Gatekeeper struct {
	heap      *energyHeap
	fastQueue *fastQueue
	aimd      *AIMD
	config    Config

	_               [cacheLineSize]byte
	active          atomic.Int64 // HOT
	_               [cacheLineSize - 8]byte
	queued          atomic.Int64 // HOT
	_               [cacheLineSize - 8]byte
	recentCreations atomic.Int64 // HOT
	_               [cacheLineSize - 8]byte
	closed          atomic.Bool
	started         atomic.Bool

	notify chan struct{}
	doneCh chan struct{}

	errorSem     chan struct{}
	errorDropped atomic.Int64
	zombieCount  atomic.Int64

	latencyShards [numShards]latencyShard

	rootCtx atomic.Value // Stores the context.Context from Start()

	inflightCount atomic.Int64 // REPLACE sync.WaitGroup
	bgWg          sync.WaitGroup
	inflightTasks *shardedMap

	_             [cacheLineSize]byte
	canaryLatency atomic.Int64 // HOT: Read by Monitor, Written by Canary
	_             [cacheLineSize - 8]byte
}

type shardedMap struct {
	shards [numShards]struct {
		sync.RWMutex
		m map[*entry]*taskState
		_ [cacheLineSize]byte
	}
}

func newShardedMap() *shardedMap {
	sm := &shardedMap{}
	for i := 0; i < numShards; i++ {
		sm.shards[i].m = make(map[*entry]*taskState)
	}
	return sm
}

func (s *shardedMap) Load(key *entry) (*taskState, bool) {
	addr := uint64(uintptr(unsafe.Pointer(key)))
	// ⚡ Bolt: Memory addresses for `entry` are exactly 128-byte aligned due to
	// cache-line padding. This means the lowest 7 bits of the pointer are ALWAYS zero.
	// We MUST right-shift by 7 to discard these dead bits before mixing, otherwise
	// the fmix64 avalanche effect is compromised, leading to severe bucket clumping
	// and lock contention on specific shards.
	addr >>= 7

	// Professional spatial entropy mixer (fmix64 style).
	addr ^= addr >> 33
	addr *= 0xff51afd7ed558ccd
	addr ^= addr >> 33
	addr *= 0xc4ceb9fe1a85ec53
	addr ^= addr >> 33
	idx := addr % numShards
	shard := &s.shards[idx]
	shard.RLock()
	v, ok := shard.m[key]
	shard.RUnlock()
	return v, ok
}

func (s *shardedMap) Store(key *entry, value *taskState) {
	addr := uint64(uintptr(unsafe.Pointer(key)))
	addr >>= 7 // discard dead 128-byte alignment bits
	addr ^= addr >> 33
	addr *= 0xff51afd7ed558ccd
	addr ^= addr >> 33
	addr *= 0xc4ceb9fe1a85ec53
	addr ^= addr >> 33
	idx := addr % numShards
	shard := &s.shards[idx]
	shard.Lock()
	shard.m[key] = value
	shard.Unlock()
}

func (s *shardedMap) Delete(key *entry) {
	addr := uint64(uintptr(unsafe.Pointer(key)))
	addr >>= 7 // discard dead 128-byte alignment bits
	addr ^= addr >> 33
	addr *= 0xff51afd7ed558ccd
	addr ^= addr >> 33
	addr *= 0xc4ceb9fe1a85ec53
	addr ^= addr >> 33
	idx := addr % numShards
	shard := &s.shards[idx]
	shard.Lock()
	delete(shard.m, key)
	shard.Unlock()
}

func (s *shardedMap) Range(f func(key *entry, value *taskState) bool) {
	for i := 0; i < numShards; i++ {
		shard := &s.shards[i]
		shard.RLock()
		for k, v := range shard.m {
			if !f(k, v) {
				shard.RUnlock()
				return
			}
		}
		shard.RUnlock()
	}
}

type latencyShard struct {
	mu      sync.Mutex
	samples [maxLatencySamples / numShards]float64
	count   int
	index   int
	_       [(cacheLineSize - ((unsafe.Sizeof(sync.Mutex{}) + unsafe.Sizeof([maxLatencySamples / numShards]float64{}) + 16) % cacheLineSize)) % cacheLineSize]byte
}

// New creates a Gatekeeper with the given configuration.
func New(cfg Config) *Gatekeeper {
	if cfg.AIMDAlpha <= 0 {
		cfg.AIMDAlpha = 1
	}
	if cfg.AIMDBeta <= 0 || cfg.AIMDBeta >= 1 {
		cfg.AIMDBeta = 0.8
	}
	if cfg.TargetLatency <= 0 {
		cfg.TargetLatency = time.Duration(Tau)
	}
	if cfg.InitialLimit == 0 {
		cfg.InitialLimit = 64
	}
	if cfg.MinConcurrency == 0 {
		cfg.MinConcurrency = 4
	}
	if cfg.MaxZombies == 0 {
		cfg.MaxZombies = 100_000
	}
	if cfg.ZombieTimeout == 0 {
		cfg.ZombieTimeout = 30 * time.Second
	}
	if cfg.CallbackTimeout <= 0 {
		cfg.CallbackTimeout = 50 * time.Millisecond
	}

	g := &Gatekeeper{
		heap: newEnergyHeap(),
		aimd: NewAIMD(
			cfg.AIMDAlpha, cfg.AIMDBeta, float64(cfg.TargetLatency.Nanoseconds()),
			float64(cfg.InitialLimit), float64(cfg.MinConcurrency), float64(cfg.MaxConcurrency),
		),
		config:        cfg,
		notify:        make(chan struct{}, 1),
		doneCh:        make(chan struct{}),
		errorSem:      make(chan struct{}, 1024),
		fastQueue:     newFastQueue(1024),
		inflightTasks: newShardedMap(),
	}
	return g
}

func (g *Gatekeeper) Submit(task Task) error {
	if g == nil {
		return fmt.Errorf("gatekeeper: cannot submit to nil gatekeeper")
	}
	if task == nil {
		return fmt.Errorf("gatekeeper: cannot submit nil task")
	}
	if !g.started.Load() {
		return fmt.Errorf("gatekeeper: Submit() called before Start()")
	}
	if g.closed.Load() {
		return ErrGateClosed
	}

	limit := g.aimd.Limit()
	if g.active.Load() < int64(limit)/2 {
		// Fairness Trade-off (The L1 Bypass):
		// This "Fast Path" skips energy heap calculations and creation pressure
		// tracking for the sake of pure nanosecond dispatch when utilization is low.
		// It favors latency over strict mathematical fairness during sub-saturation.
		//
		// [TOCTOU Tolerance]: In the window between Load() and Add(1), the limit might
		// have decreased. We accept this minor over-provisioning during rapid
		// contraction phases to maintain the lock-free property of the fast path.
		if g.active.Add(1) <= int64(limit) {
			if g.closed.Load() {
				g.releaseSlot()
				return ErrGateClosed
			}
			g.dispatchFast(task)
			return nil
		}
		g.releaseSlot()
	}

	creations := g.recentCreations.Add(1)
	e := acquireEntry(task, float64(creations))

	if task.Priority() >= PriorityUltra {
		if !g.fastQueue.Push(e) {
			// Fallback to energyHeap if L1 Ring Buffer is full
			g.heap.push(e)
		}
	} else {
		g.heap.push(e)
	}
	g.queued.Add(1)

	if g.closed.Load() {
		// Only transition state.
		// If CAS succeeds, drainHeap missed it. The task will never be dispatched.
		// Return the entry directly to the pool to avoid unnecessary GC pressure.
		if e.state.CompareAndSwap(stateQueued, stateDead) {
			g.queued.Add(-1)
			e.task = nil
			g.recentCreations.Add(-1)
			releaseEntry(e)
			return ErrGateClosed
		}
	}
	g.signal()
	return nil
}

func (g *Gatekeeper) Start(ctx context.Context) error {
	if g == nil {
		return fmt.Errorf("gatekeeper: cannot start nil gatekeeper")
	}
	if ctx == nil {
		return fmt.Errorf("gatekeeper: cannot start with nil context")
	}

	// Store context BEFORE opening the gate to prevent nil dereference
	// in rapid Submit() calls on the fast path.
	g.rootCtx.Store(ctx)

	if !g.started.CompareAndSwap(false, true) {
		return fmt.Errorf("gatekeeper: Start() called more than once")
	}
	defer close(g.doneCh)

	g.spawnBackground(func() { g.decayCreationPressure(ctx) })
	g.spawnBackground(func() { g.monitorLatency(ctx) })
	g.spawnBackground(func() { g.watchdogScan(ctx) })

	// Launch the Canary
	g.spawnBackground(func() { g.runCanary(ctx) })

	for {
		if ctx.Err() != nil {
			g.closed.Store(true)
			g.drainHeap()

			waitCtx, waitCancel := context.WithCancel(context.Background())
			go func() {
				defer waitCancel()
				// [Architectural Note]: Replaced sync.Cond with a backoff spin-wait.
				// sync.Cond.Wait() does not respect Context cancellation. If the system
				// is in a true livelock when DeathRattleTimeout fires, sync.Cond.Wait()
				// will block forever, leaking this anonymous goroutine.
				// This loop guarantees bounded exit.
				for g.inflightCount.Load() > 0 {
					select {
					case <-waitCtx.Done():
						return
					default:
						time.Sleep(1 * time.Millisecond)
					}
				}
			}()
			waitCh := waitCtx.Done()

			// Philosophy (Death Rattle Containment): When the system shuts down, user
			// tasks that have deadlocked or ignore context cancellation cannot be
			// allowed to freeze the microservice. We grant them a 'DeathRattleTimeout'
			// to finish gracefully. If they fail, we orphan the leaked goroutines and
			// forcefully exit, prioritizing process survival over memory purity.
			if g.config.DeathRattleTimeout > 0 {
				timer := time.NewTimer(g.config.DeathRattleTimeout)
				select {
				case <-waitCh:
					timer.Stop()
				case <-timer.C:
					// Cancel waitCtx to unblock the spin-wait goroutine above
					waitCancel()
					g.safeOnError(nil, fmt.Errorf("gatekeeper: DeathRattleTimeout exceeded (%v)", g.config.DeathRattleTimeout))
				}
			} else {
				<-waitCh
			}

			g.drainHeap()
			g.bgWg.Wait()
			return ctx.Err()
		}

		for g.active.Load() < int64(g.aimd.Limit()) {
			if ctx.Err() != nil {
				break
			}

			// Try Fast Queue L1 Cache first
			e := g.fastQueue.Pop()
			if e == nil {
				// Fallback to L2 energyHeap
				e = g.heap.pop()
			}

			if e == nil {
				break
			}
			g.active.Add(1)
			if !e.state.CompareAndSwap(stateQueued, stateDispatched) {
				g.releaseSlot()
				continue
			}
			g.queued.Add(-1)
			if e.yieldCount > 0 {
				e.wake <- struct{}{}
			} else {
				g.dispatch(ctx, e)
			}
		}

		select {
		case <-ctx.Done():
		case <-g.notify:
		}
	}
}

func (g *Gatekeeper) Wait() {
	if !g.started.Load() {
		return
	}
	<-g.doneCh
}

func (g *Gatekeeper) dispatchFast(task Task) {
	g.inflightCount.Add(1)
	go func() {
		e := acquireEntry(task, 0)
		e.state.Store(stateDispatched)

		baseCtx, ok := g.rootCtx.Load().(context.Context)
		if !ok {
			baseCtx = context.Background()
		}
		g.runTask(baseCtx, e, true)
	}()
}

func (g *Gatekeeper) dispatch(ctx context.Context, e *entry) {
	g.inflightCount.Add(1)
	go func() {
		g.runTask(ctx, e, false)
	}()
}

func (g *Gatekeeper) runTask(baseCtx context.Context, e *entry, isFastPath bool) {
	defer g.inflightCount.Add(-1)
	defer g.signal()

	s := acquireCtx(baseCtx, g, e)
	g.inflightTasks.Store(e, s.state)
	defer func() {
		g.inflightTasks.Delete(e)

		// Atomically mark as exiting before checking the Zombie flag.
		// This creates a synchronization barrier against the Watchdog goroutine.
		var flags uint32
		for {
			data := s.state.stateData.Load()
			flags = uint32(data)
			newData := data | uint64(flagExiting)
			if s.state.stateData.CompareAndSwap(data, newData) {
				break
			}
		}

		if s.state.ownsSlot.CompareAndSwap(true, false) {
			g.releaseSlot()
		}

		// Record final slice latency to ensure the AIMD controller doesn't go "blind".
		// For the fast-path, we always record it. For the normal path, we record it
		// only if the task failed or was zombied (inline path handle success).
		shouldRecord := isFastPath || s.state.Err() != nil || (flags&flagZombied) != 0
		if shouldRecord {
			now := NowNano()
			if sliceLatency := now - s.state.start.Load(); sliceLatency > 0 {
				g.recordLatency(time.Duration(sliceLatency))
			}
		}

		releaseEntry(e)
		releaseCtx(s.state)
	}()

	var taskErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				if g.config.OnPanic != nil {
					g.config.OnPanic(e.task, r)
				}
				taskErr = fmt.Errorf("task panicked: %v", r)
			}
		}()
		taskErr = e.task.Execute(s)
	}()

	// Normal path success latency recording (fast path always records in defer)
	if !isFastPath && s.state.Err() == nil {
		data := s.state.stateData.Load()
		flags := uint32(data)
		if (flags & flagZombied) == 0 {
			sliceLatency := NowNano() - s.state.start.Load()
			if sliceLatency > 0 {
				g.recordLatency(time.Duration(sliceLatency))
			}
		}
	}

	if taskErr != nil {
		g.safeOnError(e.task, taskErr)
	}
}

func (g *Gatekeeper) signal() {
	select {
	case g.notify <- struct{}{}:
	default:
	}
}

func (g *Gatekeeper) spawnBackground(f func()) {
	g.bgWg.Add(1)
	go func() {
		defer g.bgWg.Done()
		defer func() {
			if r := recover(); r != nil {
				if g.config.OnPanic != nil {
					g.config.OnPanic(nil, r)
				} else {
					panic(r)
				}
			}
		}()
		f()
	}()
}

func (g *Gatekeeper) releaseSlot() {
	if g.active.Add(-1) < 0 {
		g.safeOnError(nil, fmt.Errorf("gatekeeper: invariant violation - active counter went negative"))
	}
}

func (g *Gatekeeper) recordLatency(d time.Duration) {
	// ⚡ Bolt: Replaced atomic round-robin cursor with thread-local PRNG.
	// This eliminates atomic contention on a single memory address when thousands
	// of tasks complete concurrently, dramatically reducing P99 latency overhead.
	shardIdx := rand.IntN(numShards)
	shard := &g.latencyShards[shardIdx]

	shard.mu.Lock()
	shard.samples[shard.index] = float64(d.Nanoseconds())
	shard.index = (shard.index + 1) % len(shard.samples)
	if shard.count < len(shard.samples) {
		shard.count++
	}
	shard.mu.Unlock()
}

func (g *Gatekeeper) drainHeap() {
	drainOps := func(e *entry) {
		if e.state.CompareAndSwap(stateQueued, stateDead) {
			g.queued.Add(-1)

			// Detect and trigger Abort() for tasks that need it (e.g., Futures)
			if aborter, ok := e.task.(interface{ Abort() }); ok {
				aborter.Abort()
			}

			select {
			case e.wake <- struct{}{}:
			default:
			}
			if e.yieldCount == 0 {
				releaseEntry(e)
			}
		}
	}

	for {
		e := g.fastQueue.Pop()
		if e == nil {
			break
		}
		drainOps(e)
	}
	for {
		e := g.heap.pop()
		if e == nil {
			break
		}
		drainOps(e)
	}
}

func (g *Gatekeeper) watchdogScan(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond) // Increase frequency to catch tasks needing penalty
	defer ticker.Stop()

	// We pre-allocate a slice that shouldn't escape to heap often if sized appropriately
	// by using bounded collection to avoid allocating huge arrays during overload.
	type actionItem struct {
		state   *taskState
		version uint32
		elapsed int64
	}
	// Bolt: Pre-allocate the action items buffer outside the hot periodic loop
	// and reuse it to eliminate O(N) heap allocations and GC pressure every 250ms.
	items := make([]actionItem, 0, 128)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := NowNano()

			// [Architectural Note]: shardedMap.Range is not globally atomic. An entry could
			// be deleted from its shard during the iteration traversal if the task finishes.
			// This means the watchdog might observe a stale *taskState whose version has
			// expired. This is perfectly safe: the setZombied() operation is guarded by
			// a strict version/ABA check that acts as a secondary defense, ensuring we
			// never penalize or zombie a recycled slot.
			// P99 Tail Latency Assassination Fix:
			// Do NOT perform complex, time-consuming penalty calculations or log formations
			// inside the Range loop, as that holds the shardedMap.RLock, which blocks
			// ALL rapid task completions (Delete calls) on that shard, creating P99 spikes.
			// Instead, extract references to active tasks and process them lock-free.

			// Prevent memory leaks: clear pointers in the slice before resetting length
			clear(items)
			items = items[:0]
			timeout := g.config.ZombieTimeout.Nanoseconds()

			g.inflightTasks.Range(func(key *entry, value *taskState) bool {
				s := value
				data := s.stateData.Load()
				version := uint32(data >> 32)
				flags := uint32(data)

				if (flags&flagValid) != 0 && (flags&flagZombied) == 0 && (flags&flagInSyscall) == 0 {
					elapsed := now - s.dispatchedAt.Load()
					if elapsed > timeout/3 {
						items = append(items, actionItem{state: s, version: version, elapsed: elapsed})
					}
				}
				return true
			})

			// Process actions without holding the RLock
			for _, item := range items {
				s := item.state
				if item.elapsed > timeout {
					// Version validation via setZombied
					if s.setZombied(item.version) {
						s.ownsSlot.Store(false)
						g.releaseSlot()
						g.safeOnError(s.task, fmt.Errorf("gatekeeper: task zombied after %v", g.config.ZombieTimeout))
						g.signal()

						if g.zombieCount.Add(1) > int64(g.config.MaxZombies) {
							panic(fmt.Sprintf("gatekeeper: maximum zombie limit exceeded (%d)", g.config.MaxZombies))
						}
					}
				} else {
					// Linearly accumulating penalty based on the fixed tick interval, scaled by severity.
					// This turns the integral into O(N) instead of a catastrophic O(N^2) explosion.
					tickDeltaNano := float64(250 * time.Millisecond.Nanoseconds())
					severity := float64(item.elapsed-(timeout/3)) / float64(timeout) // > 0
					incrementalPenalty := tickDeltaNano * severity * PenaltyGamma

					// INJECT INTO STATE, NOT ENTRY. Safe against concurrent recycle!
					// Verify version hasn't changed before injecting
					currentData := s.stateData.Load()
					if uint32(currentData>>32) == item.version {
						s.watchdogPenalty.Add(int64(incrementalPenalty))
					}
				}
			}
		}
	}
}

func (g *Gatekeeper) safeOnError(task Task, err error) {
	if g.config.OnError == nil {
		return
	}
	select {
	case g.errorSem <- struct{}{}:
	default:
		g.errorDropped.Add(1)
		return
	}
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if g.config.OnPanic != nil {
					g.config.OnPanic(task, r)
				}
			}
			close(done)
		}()
		g.config.OnError(task, err)
	}()

	// Monitor goroutine to ensure semaphore is always released even on timeout
	go func() {
		timer := time.NewTimer(g.config.CallbackTimeout)
		defer timer.Stop()

		select {
		case <-done:
			<-g.errorSem
		case <-timer.C:
			priority := -1
			if task != nil {
				priority = task.Priority()
			}
			fmt.Fprintf(os.Stderr, "gatekeeper: OnError callback timed out (%v) for task with priority %d\n", g.config.CallbackTimeout, priority)
			<-g.errorSem
		}
	}()
}

func (g *Gatekeeper) decayCreationPressure(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(Tau) * 10)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				old := g.recentCreations.Load()
				if old <= 0 {
					break
				}
				if g.recentCreations.CompareAndSwap(old, old/2) {
					break
				}
				runtime.Gosched()
			}
		}
	}
}

func (g *Gatekeeper) rootCtxDone() <-chan struct{} {
	v := g.rootCtx.Load()
	if v == nil {
		return nil
	}
	return v.(context.Context).Done()
}

// Static Alignment Guards: Ensure no panic if we use these in arrays.
// These are rough checks; exact cacheLineSize alignment is enforced inside structs.
var _ = [1]struct{}{}[unsafe.Sizeof(Gatekeeper{})%8]
