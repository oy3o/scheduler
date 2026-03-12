package scheduler

import (
	"runtime"
	"sync/atomic"
	"unsafe"
)

// fastQueueNode is a slot in the RingQueue.
// Padded to exactly cacheLineSize to prevent False Sharing between adjacent slots.
type fastQueueNode struct {
	// [Architectural Note]: sequence is a uint64 to prevent ABA problems.
	// If reduced to uint32 to save memory, a sequence wrap-around could occur 
	// while a producer is stalled (Vyukov ABA). At 10 million ops/sec, uint32 
	// wraps in ~7 minutes, risking catastrophic data corruption. uint64 takes 
	// 58,000 years, pushing the mathematical boundary safely beyond the uptime 
	// of the physical hardware.
	sequence atomic.Uint64
	entry    atomic.Pointer[entry]
	_        [(cacheLineSize - (unsafe.Sizeof(atomic.Uint64{})+unsafe.Sizeof(atomic.Pointer[entry]{}))%cacheLineSize)%cacheLineSize]byte
}

var _ = [1]struct{}{}[unsafe.Sizeof(fastQueueNode{})%cacheLineSize]

// fastQueue is a lock-free MPMC bounded ring-buffer queue (Vyukov algorithm).
//
// Philosophy (The L1 Dispatch Cache): The energyHeap provides mathematically perfect
// fairness, but requires spin-locks and mathematical sorting. PriorityUltra tasks
// represent "drop everything and run this" emergencies. This queue acts as an L1 Cache
// bypass, dropping fairness entirely in exchange for pure O(1) lock-free nanosecond dispatch.
//
// Now switched to a Bounded Ring Buffer to eliminate the Michael-Scott "Dummy Node Leak".
// In the Michael-Scott intrusive queue, the head always points to a dummy node.
// When using a sync.Pool for 'entry' objects, a recycled entry could still be
// referenced by the queue's head, leading to memory corruption. This bounded
// buffer stores pure pointers to entries, decoupling the queue lifecycle from
// the entry lifecycle.
type fastQueue struct {
	buffer []fastQueueNode
	mask   uint64
	_      [(cacheLineSize - (unsafe.Sizeof([]fastQueueNode{})+unsafe.Sizeof(uint64(0)))%cacheLineSize)%cacheLineSize]byte
	head   atomic.Uint64
	_      [(cacheLineSize - unsafe.Sizeof(atomic.Uint64{})%cacheLineSize)%cacheLineSize]byte
	tail   atomic.Uint64
	_      [(cacheLineSize - unsafe.Sizeof(atomic.Uint64{})%cacheLineSize)%cacheLineSize]byte
}

func newFastQueue(capacity uint64) *fastQueue {
	if capacity&(capacity-1) != 0 || capacity < 2 {
		capacity = 1024 // Default safe power of 2
	}
	q := &fastQueue{
		mask:   capacity - 1,
		buffer: make([]fastQueueNode, capacity),
	}
	for i := uint64(0); i < capacity; i++ {
		q.buffer[i].sequence.Store(i)
	}
	return q
}

// Push enqueues an entry. Returns true on success, false if full.
func (q *fastQueue) Push(e *entry) bool {
	var n *fastQueueNode
	var pos uint64
	
	// Max retry for CAS contention
	for i := 0; i < 100; i++ {
		pos = q.tail.Load()
		n = &q.buffer[pos&q.mask]
		seq := n.sequence.Load()
		dif := int64(seq) - int64(pos)

		if dif == 0 {
			if q.tail.CompareAndSwap(pos, pos+1) {
				n.entry.Store(e)
				// Barrier: Pop relies on sequence reaching pos+1
				n.sequence.Store(pos + 1)
				return true
			}
		} else if dif < 0 {
			return false // Queue is genuinely full
		}

		if i > 0 && i%5 == 0 {
			runtime.Gosched()
		}
	}
	return false // High contention
}

// Pop removes and returns an entry from the head of the queue.
func (q *fastQueue) Pop() *entry {
	var n *fastQueueNode
	var pos uint64
	
	for i := 0; i < 100; i++ {
		pos = q.head.Load()
		n = &q.buffer[pos&q.mask]
		seq := n.sequence.Load()
		dif := int64(seq) - int64(pos+1)

		if dif == 0 {
			if q.head.CompareAndSwap(pos, pos+1) {
				e := n.entry.Load()
				n.entry.Store(nil)
				n.sequence.Store(pos + q.mask + 1)
				return e
			}
		} else if dif < 0 {
			// Vyukov Stalled-Producer Vulnerability Fix:
			// If a Push CASes the tail but is preempted before writing the sequence,
			// dif < 0 will trigger. We MUST check if tail implies an ongoing Push.
			if int64(q.tail.Load())-int64(pos) > 0 {
				runtime.Gosched()
				continue
			}
			return nil // Queue is genuinely empty
		}

		if i > 0 && i%5 == 0 {
			runtime.Gosched()
		}
	}
	return nil // High contention
}
