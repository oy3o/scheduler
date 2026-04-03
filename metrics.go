package scheduler

// Metrics provides a point-in-time snapshot of the Gatekeeper's internal state.
// Philosophy (Telemetry): Visibility is the precursor to control. We deliver O(1)
// atomic snapshots of the admission pipeline to allow external observability
// without introducing a measurement bias that would skew the scheduling reality.
type Metrics struct {
	Active          int
	Queued          int
	Limit           int
	Zombies         int
	RecentCreations int
	DroppedErrCount int
	CanaryDelayNano int64
}

// Metrics returns a point-in-time snapshot of the Gatekeeper's internal state.
func (g *Gatekeeper) Metrics() Metrics {
	// 🛡️ Sentinel: Prevent DoS via nil pointer dereference on public API boundary.
	if g == nil {
		return Metrics{}
	}
	return Metrics{
		Active:          int(g.active.Load()),
		Queued:          int(g.queued.Load()),
		Limit:           g.aimd.Limit(),
		Zombies:         int(g.zombieCount.Load()),
		RecentCreations: int(g.recentCreations.Load()),
		DroppedErrCount: int(g.errorDropped.Load()),
		CanaryDelayNano: g.canaryLatency.Load(),
	}
}
