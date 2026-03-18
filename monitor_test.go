package scheduler

import (
	"math"
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

func TestDetectSaturation_DecreaseAndPanic(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StrictLivelockPanic = false
	var lastErr error
	cfg.OnError = func(task Task, err error) {
		lastErr = err
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
	if lastErr == nil {
		t.Error("expected OnError to be called at 50 ticks")
	}

	// Test Clamping
	ticks = 100_001
	g.detectSaturation(&ticks, active, limit, nil)
	if ticks != 50 {
		t.Errorf("expected ticks to clamp to 50, got %d", ticks)
	}

	// Test Panic
	cfg.StrictLivelockPanic = true
	g2 := New(cfg)
	ticks = 49
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic at 50 ticks when StrictLivelockPanic is true")
		}
	}()
	g2.detectSaturation(&ticks, active, limit, nil)
}
