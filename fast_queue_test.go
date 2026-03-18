package scheduler

import (
	"testing"
)

type dummyTask struct {
	priority int
}

func (d *dummyTask) Priority() int {
	return d.priority
}

func (d *dummyTask) Execute(ctx Context) error {
	return nil
}

func TestFastQueue_PushFullCapacity(t *testing.T) {
	// Initialize a queue with capacity 4 (must be power of 2)
	capacity := uint64(4)
	q := newFastQueue(capacity)

	dummy := &dummyTask{priority: PriorityNormal}

	// Fill the queue to its capacity
	for i := uint64(0); i < capacity; i++ {
		e := acquireEntry(dummy, 0.0) // Create a dummy entry
		if !q.Push(e) {
			t.Fatalf("Failed to push entry %d, expected true when queue is not full", i)
		}
	}

	// Queue should now be full, next push must return false
	e := acquireEntry(dummy, 0.0)
	if q.Push(e) {
		t.Fatalf("Push returned true, expected false when queue is at full capacity")
	}

	// Verify that the queue handles being full correctly (doesn't panic, correctly reports full)
}
