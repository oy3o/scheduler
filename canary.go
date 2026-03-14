package scheduler

import (
	"context"
	"fmt"
	"time"
)

const (
	// CanarySleepInterval is the heartbeat of our saturation probe.
	CanarySleepInterval = 5 * time.Millisecond
	// Acceptable scheduler jitter before we consider the CPU truly saturated.
	// [Architectural Note]: Must be > 16ms to account for standard Windows
	// OS timer resolution (15.6ms). Otherwise, false positives will crush the limit.
	CanaryToleranceNano = float64(20 * time.Millisecond)
	// CanaryPanicThreshold implies the Go scheduler is completely locked.
	CanaryPanicThreshold = int64(100 * time.Millisecond)
)

// The Canary continuously probes the Go GMP scheduler's dispatch latency.
// It bypasses the OS entirely and measures the exact queueing delay
// experienced by goroutines inside the current process.
func (g *Gatekeeper) runCanary(ctx context.Context) {
	ticker := time.NewTicker(CanarySleepInterval)
	defer ticker.Stop()

	consecutivePanics := 0 // Track sustained asphyxiation

	for {
		select {
		case <-ctx.Done():
			return
		case expectedWakeup := <-ticker.C:
			// [Architectural Note]: Measure immediately upon waking.
			// expectedWakeup is when the OS timer fired. actualWakeup is when
			// the Go runtime ACTUALLY scheduled us onto an OS thread.
			queueDelayNano := time.Since(expectedWakeup).Nanoseconds()
			if queueDelayNano < 0 {
				queueDelayNano = 0
			}

			// Spike Retention: Only store if it's the maximum delay observed
			// in the current Monitor window, preventing high-frequency erasure.
			for {
				current := g.canaryLatency.Load()
				if queueDelayNano <= current {
					break
				}
				if g.canaryLatency.CompareAndSwap(current, queueDelayNano) {
					break
				}
			}

			if queueDelayNano > CanaryPanicThreshold {
				consecutivePanics++
				// Only panic if Go scheduler is dead for 5 consecutive ticks
				if consecutivePanics >= 5 && g.config.StrictLivelockPanic {
					panic(fmt.Sprintf("gatekeeper: Canary asphyxiated — Sustained Go scheduler unresponsiveness (latest delay: %v)", queueDelayNano))
				}
			} else {
				consecutivePanics = 0 // Reset on any healthy tick
			}
		}
	}
}
