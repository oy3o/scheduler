package scheduler

import (
	"math"
	"testing"
	"time"
)

func TestAIMD_Increase_Basic(t *testing.T) {
	// alpha=1, initial=10, ceil=100. 10 is far below 70% of 100.
	a := NewAIMD(1.0, 0.8, 1000, 10, 1, 100)

	a.Increase()

	if val := a.limit; val != 11.0 {
		t.Errorf("expected limit to be 11.0, got %f", val)
	}

	if val := a.Limit(); val != 11 {
		t.Errorf("expected Limit() to be 11, got %d", val)
	}
}

func TestAIMD_Increase_Deceleration(t *testing.T) {
	// alpha=1.0, ceil=100.0
	// At limit=85.0 (midway between 70% and 100% of ceil)
	// proximity = (85 - 70) / 30 = 0.5
	// step = 1.0 * (1.0 - 0.5 * 0.9) = 0.55
	a := NewAIMD(1.0, 0.8, 1000, 85, 1, 100)

	a.Increase()

	expected := 85.55
	if math.Abs(a.limit-expected) > 1e-9 {
		t.Errorf("expected limit to be around %f, got %f", expected, a.limit)
	}
}

func TestAIMD_Increase_Ceiling(t *testing.T) {
	// alpha=1, initial=99.9, ceil=100.
	a := NewAIMD(1.0, 0.8, 1000, 99.9, 1, 100)

	a.Increase()

	if a.limit != 100.0 {
		t.Errorf("expected limit to be capped at 100.0, got %f", a.limit)
	}

	if val := a.Limit(); val != 100 {
		t.Errorf("expected Limit() to be 100, got %d", val)
	}
}

func TestAIMD_Increase_NearCeiling_Decay(t *testing.T) {
	// alpha=1, ceil=100.
	// At limit=99.99, proximity is almost 1.
	// step = 1.0 * (1.0 - ~1.0 * 0.9) = ~0.1
	a := NewAIMD(1.0, 0.8, 1000, 99.99, 1, 100)

	a.Increase()

	// Should still be capped or very close to 100, but the step was small.
	if a.limit > 100.0 {
		t.Errorf("limit exceeded ceiling: %f", a.limit)
	}
}

func TestAIMD_Increase_NoCeilEffect(t *testing.T) {
	// If we set a very large ceiling, it shouldn't decelerate early.
	a := NewAIMD(1.0, 0.8, 1000, 100, 1, 1000000)

	a.Increase()

	if a.limit != 101.0 {
		t.Errorf("expected limit 101.0, got %f", a.limit)
	}
}

func TestAIMD_ObserveWithCanary_CPUChoking(t *testing.T) {
	// target=1000, initialLimit=100
	a := NewAIMD(1.0, 0.5, 1000, 100, 1, 1000)
	// Bypass cooldown
	a.lastDecreaseNano.Store(NowNano() - int64(time.Hour))

	// Trigger CPU Choking branch (canaryDelayNano > CanaryToleranceNano)
	canaryDelay := CanaryToleranceNano + 10000.0
	a.ObserveWithCanary(500, canaryDelay)

	// Since canaryDelay > CanaryToleranceNano, Decrease is called with effectiveLatency = target + canaryDelay
	// effectiveLatency = 1000 + CanaryToleranceNano + 10000
	// This will cause ratio > 1, so beta is reduced and limit decreases significantly.
	if a.limit >= 100.0 {
		t.Errorf("expected limit to decrease under CPU choking, got %f", a.limit)
	}
}

func TestAIMD_ObserveWithCanary_TasksExceedingTarget(t *testing.T) {
	// target=1000, initialLimit=100
	a := NewAIMD(1.0, 0.5, 1000, 100, 1, 1000)
	// Bypass cooldown
	a.lastDecreaseNano.Store(NowNano() - int64(time.Hour))

	// Trigger gentleDecay branch (taskP99Nano > target, canary OK)
	a.ObserveWithCanary(2000, 0)

	// gentleDecay multiplies limit by 0.95
	expected := 100.0 * 0.95
	if math.Abs(a.limit-expected) > 1e-9 {
		t.Errorf("expected gentle decay limit to be %f, got %f", expected, a.limit)
	}
}

func TestAIMD_ObserveWithCanary_CleanLogic(t *testing.T) {
	// target=1000, initialLimit=100
	a := NewAIMD(1.0, 0.5, 1000, 100, 1, 1000)

	// Trigger clean logic branch (taskP99Nano <= target, canary OK)
	a.ObserveWithCanary(500, 0)

	// Increase adds alpha (1.0)
	expected := 101.0
	if math.Abs(a.limit-expected) > 1e-9 {
		t.Errorf("expected clean logic limit to be %f, got %f", expected, a.limit)
	}
}

func TestAIMD_Decrease_Basic(t *testing.T) {
	// target=1000, initial=100, floor=1
	a := NewAIMD(1.0, 0.8, 1000, 100, 1, 1000)

	// Reset lastDecreaseNano far in the past to allow immediate decrease
	now := NowNano()
	a.lastDecreaseNano.Store(now - 10000000000) // 10 seconds ago

	// latency = 2000, ratio = 2.0
	// effectiveBeta = 0.8 / 2.0 = 0.4
	// new limit = 100 * 0.4 = 40
	a.Decrease(2000)

	if val := a.limit; val != 40.0 {
		t.Errorf("expected limit to be 40.0, got %f", val)
	}

	if val := a.Limit(); val != 40 {
		t.Errorf("expected Limit() to be 40, got %d", val)
	}
}

func TestAIMD_Decrease_Cooldown(t *testing.T) {
	// target=1000, initial=100, floor=1
	a := NewAIMD(1.0, 0.8, 1000, 100, 1, 1000)

	// Set lastDecreaseNano to NowNano() to trigger cooldown
	a.lastDecreaseNano.Store(NowNano())

	// Should be ignored due to cooldown
	a.Decrease(2000)

	if val := a.limit; val != 100.0 {
		t.Errorf("expected limit to remain 100.0 due to cooldown, got %f", val)
	}
}

func TestAIMD_Decrease_Floor(t *testing.T) {
	// target=1000, initial=10, floor=5
	a := NewAIMD(1.0, 0.8, 1000, 10, 5, 1000)

	now := NowNano()
	a.lastDecreaseNano.Store(now - 10000000000)

	// latency = 4000, ratio = 4.0
	// effectiveBeta = 0.8 / 4.0 = 0.2
	// new limit = 10 * 0.2 = 2.0 -> capped at floor 5
	a.Decrease(4000)

	if val := a.limit; val != 5.0 {
		t.Errorf("expected limit to be capped at floor 5.0, got %f", val)
	}

	if val := a.Limit(); val != 5 {
		t.Errorf("expected Limit() to be 5, got %d", val)
	}
}

func TestAIMD_Decrease_MaxPenalty(t *testing.T) {
	// target=1000, initial=100, floor=1
	a := NewAIMD(1.0, 0.8, 1000, 100, 1, 1000)

	now := NowNano()
	a.lastDecreaseNano.Store(now - 10000000000)

	// latency = 10000, ratio = 10.0
	// effectiveBeta = 0.8 / 10.0 = 0.08 -> capped at 0.2
	// new limit = 100 * 0.2 = 20
	a.Decrease(10000)

	if val := a.limit; val != 20.0 {
		t.Errorf("expected limit to be 20.0 (max penalty cap), got %f", val)
	}
}
