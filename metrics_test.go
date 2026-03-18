package scheduler

import (
	"testing"
)

func TestGatekeeper_Metrics(t *testing.T) {
	g := New(DefaultConfig())

	// Artificially mutate the internal counters.
	g.active.Store(10)
	g.queued.Store(5)
	g.zombieCount.Store(2)
	g.recentCreations.Store(15)
	g.errorDropped.Store(1)
	g.canaryLatency.Store(1000)

	// Fetch metrics.
	m := g.Metrics()

	// Assert the metrics match the mutated state.
	if m.Active != 10 {
		t.Errorf("Expected Active to be 10, got %d", m.Active)
	}
	if m.Queued != 5 {
		t.Errorf("Expected Queued to be 5, got %d", m.Queued)
	}
	// InitialLimit from DefaultConfig is 64. AIMD controller uses it.
	if m.Limit != 64 {
		t.Errorf("Expected Limit to be 64, got %d", m.Limit)
	}
	if m.Zombies != 2 {
		t.Errorf("Expected Zombies to be 2, got %d", m.Zombies)
	}
	if m.RecentCreations != 15 {
		t.Errorf("Expected RecentCreations to be 15, got %d", m.RecentCreations)
	}
	if m.DroppedErrCount != 1 {
		t.Errorf("Expected DroppedErrCount to be 1, got %d", m.DroppedErrCount)
	}
	if m.CanaryDelayNano != 1000 {
		t.Errorf("Expected CanaryDelayNano to be 1000, got %d", m.CanaryDelayNano)
	}
}
