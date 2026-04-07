package scheduler

import (
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestSortedPercentileMut(t *testing.T) {
	tests := []struct {
		name     string
		data     []float64
		p        float64
		expected float64
	}{
		{"Empty", []float64{}, 0.5, 0},
		{"Single", []float64{10}, 0.5, 10},
		{"P0", []float64{10, 20, 30}, 0.0, 10},
		{"P1", []float64{10, 20, 30}, 1.0, 30},
		{"P0.5", []float64{10, 20, 30}, 0.5, 20},
		{"Interpolated", []float64{10, 20, 30}, 0.25, 15},
		{"UnsortedInput", []float64{30, 10, 20}, 0.5, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Copy data because it's mutated
			data := make([]float64, len(tt.data))
			copy(data, tt.data)
			got := sortedPercentileMut(data, tt.p)
			if math.Abs(got-tt.expected) > 1e-9 {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

func TestDetectSaturation_Basic(t *testing.T) {
	g := New(DefaultConfig())
	ticks := 0
	limit := 100.0
	active := 50.0 // Under 80%

	// 1. Not saturated (active < 0.8 * limit)
	if g.detectSaturation(&ticks, active, limit, nil) {
		t.Error("should not detect saturation when active < 0.8 * limit")
	}
	if ticks != 0 {
		t.Errorf("expected ticks to be 0, got %d", ticks)
	}

	// 2. Not saturated (samples collected)
	active = 90.0
	samples := []float64{1.0}
	if g.detectSaturation(&ticks, active, limit, samples) {
		t.Error("should not detect saturation when samples are present")
	}
	if ticks != 0 {
		t.Errorf("expected ticks to be 0, got %d", ticks)
	}

	// 3. Saturated
	if !g.detectSaturation(&ticks, active, limit, nil) {
		t.Error("should detect saturation when active >= 0.8 * limit and no samples")
	}
	if ticks != 1 {
		t.Errorf("expected ticks to be 1, got %d", ticks)
	}
}

func BenchmarkSortedPercentileMut_FracZero(b *testing.B) {
	data := make([]float64, 4096)
	for i := range data {
		data[i] = float64(i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// p=0.5 exactly on an array of size 4096 gives an exact fractional index when len-1 is odd
		// Wait, idx = (4096-1)*0.5 = 2047.5, so frac is 0.5.
		// To hit frac == 0, we need idx to be an integer.
		// So (len - 1) * p = integer.
		// If len = 4097, (4097 - 1) * 0.5 = 2048 exactly.
		// Let's use p=0.5 and len=4097

		// In Go benchmarks, we need to be careful to not mutate the array in a way that breaks subsequent loops
		// But sortedPercentileMut mutates the array (QuickSelect partitions it).
		// Since we just want to measure the overhead, we'll re-copy the array.
		b.StopTimer()
		testData := make([]float64, 4097)
		copy(testData, data)
		b.StartTimer()

		sortedPercentileMut(testData, 0.5)
	}
}

func TestDetectSaturation_DecreaseAndPanic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StrictLivelockPanic = false
	var lastErr atomic.Pointer[error]
	cfg.OnError = func(task Task, err error) {
		lastErr.Store(&err)
	}
	g := New(cfg)

	ticks := 0
	limit := 100.0
	active := 90.0
	initialLimit := g.aimd.Limit()

	// Wait for AIMD initial cooldown (100ms) to ensure Decrease works.
	// We also need to wait a bit more because NowNano might be slightly different
	// from what NewAIMD used.
	time.Sleep(200 * time.Millisecond)

	// Advance to 5 ticks to trigger Decrease
	for i := 0; i < 5; i++ {
		g.detectSaturation(&ticks, active, limit, nil)
	}
	if ticks != 5 {
		t.Errorf("expected ticks 5, got %d", ticks)
	}
	if g.aimd.Limit() >= initialLimit {
		t.Errorf("expected limit to decrease, but it is %d (initial %d)", g.aimd.Limit(), initialLimit)
	}

	// Advance to 50 ticks to trigger OnError.
	// We don't need to sleep here because we already verified Decrease above,
	// and we just want to reach the tick threshold for the error.
	for i := 0; i < 45; i++ {
		g.detectSaturation(&ticks, active, limit, nil)
	}
	if ticks != 50 {
		t.Errorf("expected ticks 50, got %d", ticks)
	}

	// OnError is called in a goroutine, wait a bit
	time.Sleep(10 * time.Millisecond)
	if lastErr.Load() == nil {
		t.Error("expected OnError to be called at 50 ticks")
	}

	// Test Clamping
	ticks = 100_001
	g.detectSaturation(&ticks, active, limit, nil)
	if ticks != 50 {
		t.Errorf("expected ticks to clamp to 50, got %d", ticks)
	}

	// Test Degradation (Cardiogenic Shock Mode)
	cfg.StrictLivelockPanic = true
	g2 := New(cfg)
	ticks = 49

	// Wait for AIMD initial cooldown so Decrease() actually functions for g2
	time.Sleep(200 * time.Millisecond)

	g2.detectSaturation(&ticks, active, limit, nil)

	if uint64(g2.aimd.Limit()) > uint64(g2.config.MinConcurrency) {
		t.Errorf("expected limit to forcefully degrade to MinConcurrency (%v), but it is %v", g2.config.MinConcurrency, g2.aimd.Limit())
	}
}
