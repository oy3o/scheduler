package scheduler

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"unsafe"
)

// entryHot contains the frequently updated metadata for an entry.
type entryHot struct {
	task             Task
	energy           float64
	runtimeNano      int64         // int64 accumulation prevents IEEE-754 mantissa absorption
	yieldCount       int           // cooperative yield counter S_i
	creationPressure float64       // C_i snapshot at submission time
	wake             chan struct{} // signals Yield() resumption
	state            atomic.Uint32 // stateQueued → stateDispatched → stateDead
	generation       uint64        // incremented on reuse to invalidate stale callbacks
}

// entry is a heap element wrapping a task with its scheduling metadata.
type entry struct {
	entryHot

	// Pad to ensure cache line isolation.
	// Philosophy (Mechanical Sympathy): If adjacent entries share a cache line (128B),
	// concurrent atomic updates (like penaltyNano) by the watchdog will trigger
	// massive 'false sharing' cache-invalidation storms across CPU cores.
	_ [(cacheLineSize - (unsafe.Sizeof(entryHot{}) % cacheLineSize)) % cacheLineSize]byte
}

var _ = [1]struct{}{}[unsafe.Sizeof(entry{})%cacheLineSize]

const (
	stateQueued uint32 = iota
	stateDispatched
	stateDead
)

type energyShard struct {
	mu      sync.Mutex
	entries []*entry
	_       [(cacheLineSize - (unsafe.Sizeof(sync.Mutex{})+unsafe.Sizeof([]*entry{}))%cacheLineSize) % cacheLineSize]byte // cache-line padding to prevent false sharing
}

// energyHeap is a sharded min-heap ordered by energy to reduce lock contention.
//
// Philosophy (The Shattered Bottleneck): A single global heap lock scales poorly
// beyond 16-32 cores. By sharding the heap, we fracture the contention domain.
// Coupled with the Power of Two Random Choices during extraction, we achieve
// near O(1) contention while maintaining a strong probabilistic fairness boundary.
type energyHeap struct {
	shards [numShards]*energyShard
}

var entryPool = sync.Pool{
	New: func() any {
		return &entry{
			entryHot: entryHot{
				wake: make(chan struct{}, 1),
			},
		}
	},
}

// acquireEntry retrieves an entry from the pool and initializes it for a new task.
func acquireEntry(task Task, creationPressure float64) *entry {
	e := entryPool.Get().(*entry)
	e.task = task
	e.creationPressure = creationPressure
	e.energy = Energy(0, Weight(task.Priority()), creationPressure, 0)
	e.runtimeNano = 0
	e.yieldCount = 0
	e.state.Store(stateQueued)
	e.generation++ // ABA protection

	// Safety: Purge any phantom signals from ghost goroutines without
	// allocating new memory, preserving the zero-allocation hot path.
	select {
	case <-e.wake:
	default:
	}

	return e
}

// releaseEntry returns an entry to the pool after clearing task reference.
func releaseEntry(e *entry) {
	if e == nil {
		return
	}
	e.task = nil // prevent memory leak
	entryPool.Put(e)
}

func newEnergyHeap() *energyHeap {
	h := &energyHeap{}
	for i := range h.shards {
		h.shards[i] = &energyShard{}
	}
	return h
}

func (h *energyHeap) push(e *entry) {
	idx := rand.Uint32N(numShards) // Random choice
	h.shards[idx].push(e)
}

func (h *energyHeap) pop() *entry {
	// Fast path: 2-choice (Power of Two Random Choices)
	// Rather than locking all shards or searching for the absolute global minimum,
	// we randomly sample two shards and pick the best. Mathematically, this provides
	// an exponentially better maximum load balance than 1-choice, while avoiding
	// the catastrophic contention of an N-choice global scan.
	idx1 := rand.Uint32N(numShards)
	idx2 := rand.Uint32N(numShards)
	if idx1 == idx2 {
		idx2 = (idx1 + 1) % numShards
	}

	// Always lock in ascending index order to prevent deadlocks
	lock1, lock2 := idx1, idx2
	if lock1 > lock2 {
		lock1, lock2 = lock2, lock1
	}

	s1, s2 := h.shards[lock1], h.shards[lock2]
	s1.mu.Lock()
	s2.mu.Lock()

	var e *entry
	if len(s1.entries) > 0 && len(s2.entries) > 0 {
		if s1.entries[0].energy < s2.entries[0].energy {
			e = s1.pop()
		} else {
			e = s2.pop()
		}
	} else if len(s1.entries) > 0 {
		e = s1.pop()
	} else if len(s2.entries) > 0 {
		e = s2.pop()
	}

	s2.mu.Unlock()
	s1.mu.Unlock()

	if e != nil {
		return e
	}

	// Slow path: scan remaining shards if both random choices were empty.
	// [Architectural Note]: This is a Best-Effort scan. We do not hold multiple
	// shard locks at once to avoid deadlocks (unless during the initial 2-choice).
	// A shard sampled as empty could be populated immediately after we release its lock;
	// we accept this slight inaccuracy to preserve O(1) lock-free-like scaling.
	for i := uint32(0); i < numShards; i++ {
		shardIdx := (idx1 + i) % numShards
		if shardIdx == idx1 || shardIdx == idx2 {
			continue
		}
		s := h.shards[shardIdx]
		s.mu.Lock()
		if len(s.entries) > 0 {
			e = s.pop()
			s.mu.Unlock()
			return e
		}
		s.mu.Unlock()
	}

	return nil
}

// LenSync returns the global element count. (O(numShards) locking)
func (h *energyHeap) LenSync() int {
	total := 0
	for i := range h.shards {
		h.shards[i].mu.Lock()
		total += len(h.shards[i].entries)
		h.shards[i].mu.Unlock()
	}
	return total
}

func (h *energyShard) push(e *entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, e)
	h.siftUp(len(h.entries) - 1)
}

func (h *energyShard) pop() *entry {
	// Caller MUST hold h.mu lock
	n := len(h.entries)
	if n == 0 {
		return nil
	}
	min := h.entries[0]
	last := n - 1
	h.entries[0] = h.entries[last]
	h.entries[last] = nil // prevent memory leak
	h.entries = h.entries[:last]
	if last > 0 {
		h.siftDown(0)
	}
	return min
}

// --- strictly-typed sift operations (called only under lock) ---

// siftUp restores the heap property by moving the element at index i up.
// It uses a single-assignment "hole" optimization instead of full swaps,
// reducing memory writes in the hot path.
func (h *energyShard) siftUp(i int) {
	e := h.entries[i]
	for i > 0 {
		parent := (i - 1) / 2
		if h.entries[parent].energy <= e.energy {
			break
		}
		// Move parent down, avoiding a full swap and extra reads
		h.entries[i] = h.entries[parent]
		i = parent
	}
	h.entries[i] = e
}

// siftDown restores the heap property by moving the element at index i down.
// It uses a single-assignment "hole" optimization instead of full swaps,
// reducing memory writes in the hot path.
func (h *energyShard) siftDown(i int) {
	n := len(h.entries)
	e := h.entries[i]
	for {
		child := 2*i + 1 // left child
		if child >= n {
			break
		}
		// Find the smaller of the two children
		if right := child + 1; right < n && h.entries[right].energy < h.entries[child].energy {
			child = right
		}
		// If the current element is smaller than or equal to the smallest child, we're done
		if e.energy <= h.entries[child].energy {
			break
		}
		// Move child up, avoiding a full swap
		h.entries[i] = h.entries[child]
		i = child
	}
	h.entries[i] = e
}
