package scheduler

import (
	"math/rand/v2"
	"sync"
	"testing"
	"unsafe"
)

type mockTask struct {
	priority int
}

func (m *mockTask) Priority() int             { return m.priority }
func (m *mockTask) Execute(ctx Context) error { return nil }

func TestAcquireReleaseEntry(t *testing.T) {
	task := &mockTask{priority: 100}
	creationPressure := 5.0
	e := acquireEntry(task, creationPressure)

	if e.task != task {
		t.Errorf("expected task %v, got %v", task, e.task)
	}
	if e.creationPressure != creationPressure {
		t.Errorf("expected creationPressure %v, got %v", creationPressure, e.creationPressure)
	}
	if e.state.Load() != stateQueued {
		t.Errorf("expected state %v, got %v", stateQueued, e.state.Load())
	}

	// Check if energy is initialized (basic check)
	if e.energy == 0 && creationPressure > 0 {
		// Energy should be non-zero if creationPressure > 0 due to PenaltyGamma * Tau * log(C_i + 1)
		// Actually Energy(0, Weight(100), 5.0, 0) = 1.0/Weight(100) + PenaltyGamma*Tau*log(6) - 0
		// Weight(100) is 1.0 + log2(101)*2.0 approx 1 + 6.6*2 = 14.2
		// So energy should be > 0.
		if e.energy <= 0 {
			t.Errorf("expected energy > 0, got %v", e.energy)
		}
	}

	// Release and check memory leak prevention
	releaseEntry(e)
	if e.task != nil {
		t.Errorf("expected e.task to be nil after release, got %v", e.task)
	}
}

func TestEnergyShard_MinHeap(t *testing.T) {
	shard := &energyShard{}

	energies := []float64{10.5, 5.2, 100.1, 2.0, 50.0}
	for _, en := range energies {
		shard.push(&entry{entryHot: entryHot{energy: en}})
	}

	if len(shard.entries) != 5 {
		t.Errorf("expected 5 entries, got %d", len(shard.entries))
	}

	// Pop and verify min-heap order
	lastEnergy := -1.0
	for i := 0; i < 5; i++ {
		e := shard.pop()
		if e == nil {
			t.Fatalf("expected entry at pop %d, got nil", i)
		}
		if e.energy < lastEnergy {
			t.Errorf("min-heap property violated: %v < %v", e.energy, lastEnergy)
		}
		lastEnergy = e.energy
	}

	if len(shard.entries) != 0 {
		t.Errorf("expected 0 entries after all pops, got %d", len(shard.entries))
	}
}

func TestEnergyHeap_Basic(t *testing.T) {
	h := newEnergyHeap()

	task := &mockTask{priority: 1}
	e1 := &entry{entryHot: entryHot{energy: 10.0, task: task}}
	e2 := &entry{entryHot: entryHot{energy: 5.0, task: task}}
	e3 := &entry{entryHot: entryHot{energy: 15.0, task: task}}

	h.push(e1)
	h.push(e2)
	h.push(e3)

	if total := h.LenSync(); total != 3 {
		t.Errorf("expected 3 entries total, got %d", total)
	}

	// Pop all
	p1 := h.pop()
	p2 := h.pop()
	p3 := h.pop()
	p4 := h.pop()

	if p1 == nil || p2 == nil || p3 == nil {
		t.Fatalf("expected 3 non-nil pops, got %v, %v, %v", p1, p2, p3)
	}
	if p4 != nil {
		t.Errorf("expected 4th pop to be nil, got %v", p4)
	}

	// Best effort check: lowest energy (5.0) should likely be popped early if sampled.
	// Since there are only 3 elements in 8 shards, it's likely they are in different shards.
	// pop() samples 2 shards.
}

func TestEnergyHeap_SlowPathScan(t *testing.T) {
	h := newEnergyHeap()
	// Force all elements into one specific shard that is NOT picked by initial rand.Uint32N
	// We can't easily control rand in pop() without mocking, but we can verify it works
	// by putting an element in only one shard and calling pop until we get it.

	e := &entry{entryHot: entryHot{energy: 1.0, task: &mockTask{}}}
	h.shards[7].push(e) // Put in last shard

	// pop should eventually find it via slow path if it misses shard 7 in the first 2 choices.
	p := h.pop()
	if p != e {
		t.Errorf("expected to pop the single entry, got %v", p)
	}
}

func TestEntry_Alignment(t *testing.T) {
	// Verify cache line alignment to prevent false sharing
	e := entry{}
	size := unsafe.Sizeof(e)
	if size%cacheLineSize != 0 {
		t.Errorf("expected entry size to be multiple of %d, got %d", cacheLineSize, size)
	}

	shard := energyShard{}
	if unsafe.Sizeof(shard)%cacheLineSize != 0 {
		t.Errorf("expected energyShard size to be multiple of %d, got %d", cacheLineSize, unsafe.Sizeof(shard))
	}
}

func TestEnergyHeap_Concurrent(t *testing.T) {
	h := newEnergyHeap()
	const numOps = 1000
	var wg sync.WaitGroup

	// Concurrent pushers
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			h.push(&entry{entryHot: entryHot{energy: rand.Float64() * 100, task: &mockTask{}}})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			h.push(&entry{entryHot: entryHot{energy: rand.Float64() * 100, task: &mockTask{}}})
		}
	}()

	// Concurrent poppers
	results := make(chan *entry, numOps*2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			for {
				if e := h.pop(); e != nil {
					results <- e
					break
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			for {
				if e := h.pop(); e != nil {
					results <- e
					break
				}
			}
		}
	}()

	wg.Wait()
	close(results)

	count := 0
	for range results {
		count++
	}
	if count != numOps*2 {
		t.Errorf("expected %d entries popped, got %d", numOps*2, count)
	}
	if h.LenSync() != 0 {
		t.Errorf("expected heap to be empty, got %d", h.LenSync())
	}
}
