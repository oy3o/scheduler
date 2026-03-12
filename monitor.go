package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// monitorLatency samples execution latencies and feeds the AIMD controller.
// Runs as a background goroutine started by Gatekeeper.Start().
func (g *Gatekeeper) monitorLatency(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Snapshot active/limit before extraction so the overload check
			// reflects the state that produced the samples, not a later one.
			currentActive := float64(g.active.Load())
			currentLimit := float64(g.aimd.Limit())

			// Snapshot and extract samples from all shards
			var samples []float64
			for i := 0; i < numShards; i++ {
				shard := &g.latencyShards[i]
				shard.mu.Lock()
				if shard.count > 0 {
					if samples == nil {
						samples = make([]float64, 0, maxLatencySamples)
					}
					// Extract all valid samples from this shard in precise chronological order
					if shard.count < len(shard.samples) {
						// Buffer not yet wrapped
						samples = append(samples, shard.samples[:shard.count]...)
					} else {
						// Buffer wrapped: extract oldest samples first [index:] then newest [:index]
						samples = append(samples, shard.samples[shard.index:]...)
						samples = append(samples, shard.samples[:shard.index]...)
					}
					// Reset shard state
					shard.count = 0
					shard.index = 0
				}
				shard.mu.Unlock()
			}

			// Silent Saturation Detection (Survivorship Bias):
			if g.detectSaturation(&ticks, currentActive, currentLimit, samples) {
				continue
			}

			// ticks = 0  // [Architectural Note]: We no longer instantly reset to 0
			// to prevent high-frequency jitter from hiding persistent boundary saturation.
			if ticks > 0 {
				ticks--
			}
			if len(samples) == 0 {
				continue
			}

			// Require a minimum sample count to avoid noisy percentiles.
			if len(samples) < g.config.MinAIMDSamples {
				continue
			}

			p99 := sortedPercentileMut(samples, 0.99)

			// Only adjust capacity when the pipeline is genuinely saturated.
			if currentActive >= currentLimit*0.8 {
				g.aimd.Observe(p99)
			}
			g.signal()
		}
	}
}

func (g *Gatekeeper) detectSaturation(ticks *int, active, limit float64, samples []float64) bool {
	if active >= limit*0.8 && len(samples) == 0 {
		*ticks++

		// Clamp ticks to prevent integer overflow and preserve modulo logic (long-running edge case)
		if *ticks > 100_000 {
			*ticks = 50 // Clamp to stable warning zone
		}

		// AIMD contraction every 5 ticks (~500ms).
		if *ticks%5 == 0 {
			syntheticLatency := float64(g.config.TargetLatency.Nanoseconds()) * 2.0
			g.aimd.Observe(syntheticLatency)
			g.signal()
		}

		// Asphyxiation alarm at 50 ticks (~5 seconds).
		if *ticks >= 50 && *ticks%10 == 0 {
			err := fmt.Errorf(
				"gatekeeper: possible livelock — %d/%d slots occupied with zero throughput for %.1fs; "+
					"tasks may be deadlocked or performing heavy CPU work without yielding",
				int(active), int(limit), float64(*ticks)*0.1,
			)

			if g.config.StrictLivelockPanic {
				panic(err.Error())
			} else {
				g.safeOnError(nil, err)
			}
		}
		return true
	}
	return false
}

func sortedPercentileMut(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}

	sort.Float64s(data)

	if p >= 1.0 {
		return data[len(data)-1]
	}
	if p <= 0.0 {
		return data[0]
	}

	idx := float64(len(data)-1) * p
	lo := int(idx)
	hi := lo + 1
	if hi >= len(data) {
		return data[lo]
	}

	frac := idx - float64(lo)
	return data[lo] + frac*(data[hi]-data[lo])
}
