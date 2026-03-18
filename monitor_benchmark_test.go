package scheduler

import (
	"context"
	"testing"
	"time"
)

// BenchmarkMonitorLatency_Allocation benchmarks the slice allocation inside monitorLatency's core logic.
// We extract the slice allocation and appending logic here to benchmark it properly,
// since monitorLatency is a background goroutine with a 100ms ticker.
func BenchmarkMonitorLatency_Allocation(b *testing.B) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Start(ctx)

	// Pre-fill shards to simulate heavy load
	for i := 0; i < numShards; i++ {
		shard := &g.latencyShards[i]
		shard.mu.Lock()
		for j := 0; j < 50; j++ {
			shard.samples[shard.index] = float64(time.Millisecond.Nanoseconds())
			shard.index = (shard.index + 1) % len(shard.samples)
			if shard.count < len(shard.samples) {
				shard.count++
			}
		}
		shard.mu.Unlock()
	}

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		// Mock the logic from the hot loop in monitor.go:33
		var samplesBuffer []float64

		// Snapshot and extract samples from all shards
		for i := 0; i < numShards; i++ {
			shard := &g.latencyShards[i]
			shard.mu.Lock()
			if shard.count > 0 {
				if samplesBuffer == nil {
					samplesBuffer = make([]float64, 0, maxLatencySamples)
				}
				// Extract all valid samples from this shard in precise chronological order
				if shard.count < len(shard.samples) {
					// Buffer not yet wrapped
					samplesBuffer = append(samplesBuffer, shard.samples[:shard.count]...)
				} else {
					// Buffer wrapped: extract oldest samples first [index:] then newest [:index]
					samplesBuffer = append(samplesBuffer, shard.samples[shard.index:]...)
					samplesBuffer = append(samplesBuffer, shard.samples[:shard.index]...)
				}
				// Mock reset shard state for benchmark (normally we do this, but for benchmark we need them filled next iteration)
				// shard.count = 0
				// shard.index = 0
			}
			shard.mu.Unlock()
		}

		_ = samplesBuffer
	}
}

func BenchmarkMonitorLatency_Optimized(b *testing.B) {
	g := New(DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Start(ctx)

	// Pre-fill shards to simulate heavy load
	for i := 0; i < numShards; i++ {
		shard := &g.latencyShards[i]
		shard.mu.Lock()
		for j := 0; j < 50; j++ {
			shard.samples[shard.index] = float64(time.Millisecond.Nanoseconds())
			shard.index = (shard.index + 1) % len(shard.samples)
			if shard.count < len(shard.samples) {
				shard.count++
			}
		}
		shard.mu.Unlock()
	}

	b.ResetTimer()
	b.ReportAllocs()

	samplesBuffer := make([]float64, 0, maxLatencySamples)
	for n := 0; n < b.N; n++ {
		samplesBuffer = samplesBuffer[:0]

		// Snapshot and extract samples from all shards
		for i := 0; i < numShards; i++ {
			shard := &g.latencyShards[i]
			shard.mu.Lock()
			if shard.count > 0 {
				// Extract all valid samples from this shard in precise chronological order
				if shard.count < len(shard.samples) {
					// Buffer not yet wrapped
					samplesBuffer = append(samplesBuffer, shard.samples[:shard.count]...)
				} else {
					// Buffer wrapped: extract oldest samples first [index:] then newest [:index]
					samplesBuffer = append(samplesBuffer, shard.samples[shard.index:]...)
					samplesBuffer = append(samplesBuffer, shard.samples[:shard.index]...)
				}
			}
			shard.mu.Unlock()
		}

		_ = samplesBuffer
	}
}
