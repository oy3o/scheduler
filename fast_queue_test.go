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

func TestFastQueue_Pop(t *testing.T) {
	capacity := uint64(4)
	q := newFastQueue(capacity)
	dummy := &dummyTask{priority: PriorityNormal}

	// 1. Pop from empty queue should return nil
	if e := q.Pop(); e != nil {
		t.Fatal("Pop from empty queue should return nil")
	}

	// 2. FIFO order verification
	entries := make([]*entry, capacity)
	for i := uint64(0); i < capacity; i++ {
		entries[i] = acquireEntry(dummy, 0.0)
		if !q.Push(entries[i]) {
			t.Fatalf("Failed to push entry %d", i)
		}
	}

	for i := uint64(0); i < capacity; i++ {
		e := q.Pop()
		if e == nil {
			t.Fatalf("Pop returned nil for entry %d", i)
		}
		if e != entries[i] {
			t.Errorf("Popped entry %d does not match pushed entry", i)
		}
	}

	if e := q.Pop(); e != nil {
		t.Fatal("Pop from empty queue after being full should return nil")
	}

	// 3. Ring buffer wrap-around
	// Push 4, Pop 2, Push 2, Pop 4
	for i := 0; i < 4; i++ {
		if !q.Push(acquireEntry(dummy, 0.0)) {
			t.Fatalf("Failed to push entry %d during wrap-around test", i)
		}
	}

	for i := 0; i < 2; i++ {
		if e := q.Pop(); e == nil {
			t.Fatalf("Failed to pop entry %d during wrap-around test", i)
		}
	}

	for i := 0; i < 2; i++ {
		if !q.Push(acquireEntry(dummy, 0.0)) {
			t.Fatalf("Failed to push entry %d after wrap-around", i+4)
		}
	}

	for i := 0; i < 4; i++ {
		if e := q.Pop(); e == nil {
			t.Fatalf("Failed to pop entry %d during final wrap-around check", i)
		}
	}

	if e := q.Pop(); e != nil {
		t.Fatal("Pop should be empty after wrap-around test")
	}
}
