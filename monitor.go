package scheduler

import (
	"context"
	"fmt"
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

// sortedPercentileMut finds the p-th percentile of data mutating the array.
// Replaced sort.Float64s (O(N log N)) with QuickSelect (O(N) expected time)
// which dramatically reduces CPU block time in the monitor loop when N > 1000.
func sortedPercentileMut(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}

	if p >= 1.0 {
		max := data[0]
		for _, v := range data[1:] {
			if v > max {
				max = v
			}
		}
		return max
	}
	if p <= 0.0 {
		min := data[0]
		for _, v := range data[1:] {
			if v < min {
				min = v
			}
		}
		return min
	}

	idx := float64(len(data)-1) * p
	lo := int(idx)
	hi := lo + 1

	if hi >= len(data) {
		return quickSelect(data, lo)
	}

	vLo := quickSelect(data, lo)

	// vHi is the minimum of elements AFTER lo
	vHi := data[lo+1]
	for i := lo + 2; i < len(data); i++ {
		if data[i] < vHi {
			vHi = data[i]
		}
	}

	frac := idx - float64(lo)
	return vLo + frac*(vHi-vLo)
}

func quickSelect(a []float64, k int) float64 {
	left, right := 0, len(a)-1
	for {
		if left == right {
			return a[left]
		}
		// A simple middle element pivot to avoid Rand overhead in tight loop
		pivotIndex := left + (right-left)/2
		pivotIndex = partition(a, left, right, pivotIndex)
		if k == pivotIndex {
			return a[k]
		} else if k < pivotIndex {
			right = pivotIndex - 1
		} else {
			left = pivotIndex + 1
		}
	}
}

func partition(a []float64, left, right, pivotIndex int) int {
	pivotValue := a[pivotIndex]
	a[pivotIndex], a[right] = a[right], a[pivotIndex] // Move pivot to end
	storeIndex := left
	for i := left; i < right; i++ {
		if a[i] < pivotValue {
			a[storeIndex], a[i] = a[i], a[storeIndex]
			storeIndex++
		}
	}
	a[right], a[storeIndex] = a[storeIndex], a[right] // Move pivot to its final place
	return storeIndex
}
