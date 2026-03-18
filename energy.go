package scheduler

import "math"

// Scheduling constants anchored to the base latency τ = 5ms.
const (
	Tau           = 5_000_000 // 5ms in nanoseconds — the system's temporal heartbeat
	PriorityScale = 2.0       // Weight scaling factor: W(p) = 1 + log₂(p+1) * scale
	PenaltyGamma  = 0.1       // Creation pressure sensitivity
	FeedbackBeta  = 0.3       // IO/interactive reward sensitivity
)

// Precomputed lookup tables eliminate FPU transcendental calls (math.Log/Log2)
// from the scheduling hot path.
//
// Philosophy (The Fast Path): In a scheduler designed for millions of events per second,
// mathematical purity must bow to mechanical sympathy. We accept bounded table approximations
// to reduce per-dispatch overhead to O(1) arithmetic, ensuring the scheduling mechanism
// never becomes the bottleneck it was designed to alleviate.
var (
	weightTable      [2001]float64
	yieldBonusTable  [101]float64
	creationPenTable [1024]float64
)

func init() {
	for p := 0; p <= 2000; p++ {
		w := 1.0 + math.Log2(float64(p)+1.0)*PriorityScale
		if w < 1e-9 {
			w = 1e-9
		}
		weightTable[p] = w
	}
	for y := 0; y <= 100; y++ {
		yieldBonusTable[y] = math.Log(float64(y) + 1.0)
	}
	for c := 0; c < 1024; c++ {
		creationPenTable[c] = math.Log(float64(c) + 1.0)
	}
}

// Weight converts a discrete priority level into a continuous scheduling weight.
// Higher priority → larger weight → slower energy growth → more CPU share.
// Uses logarithmic scaling to prevent high-priority tasks from exponentially
// starving lower-priority ones.
func Weight(priority int) float64 {
	if priority < 0 {
		return weightTable[0]
	}
	if priority > 2000 {
		return weightTable[2000]
	}
	return weightTable[priority]
}

// Energy computes the unified scheduling score for a task.
// Lower energy → scheduled sooner.
//
//	E_i = (R_i / W_i) + γ·τ·log(C_i + 1) − β·τ·log(S_i + 1)
//
// Philosophy (Transient Praise): The yield bonus (FeedbackBeta) is intentionally transient.
// As runtimeNano (R_i) grows over the lifetime of a long-running task, the linear
// fairness term dominates the static logarithmic bonus. This prevents cooperative
// "yield-spamming" tasks from permanently monopolizing the CPU away from fresh arrivals.
func Energy(runtimeNano float64, weight float64, creationPressure float64, yieldCount int) float64 {
	runtimeMs := runtimeNano / 1e6
	if runtimeMs < 1.0 {
		runtimeMs = 1.0
	}

	if weight <= 0 {
		weight = 1e-9
	}

	fairness := runtimeMs / weight

	// Creation penalty: logarithmic to dampen burst sensitivity.
	// Falls back to math.Log only for extreme bursts (>1024 concurrent submissions).
	var pressureLog float64
	if creationPressure < 0 {
		pressureLog = 0
	} else if creationPressure < 1024 {
		pressureLog = creationPenTable[int(creationPressure)]
	} else {
		pressureLog = math.Log(creationPressure + 1.0)
	}
	penalty := PenaltyGamma * Tau * pressureLog

	// Yield bonus: capped at 100 yields to prevent cooperative tasks from
	// accumulating an unbounded advantage over new arrivals.
	if yieldCount < 0 {
		yieldCount = 0
	}
	if yieldCount > 100 {
		yieldCount = 100
	}
	bonus := FeedbackBeta * Tau * yieldBonusTable[yieldCount]

	return fairness + penalty - bonus
}
