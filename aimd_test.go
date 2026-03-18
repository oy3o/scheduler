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
