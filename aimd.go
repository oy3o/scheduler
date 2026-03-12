package scheduler

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// AIMD implements Additive-Increase / Multiplicative-Decrease concurrency control.
// Mutations are serialized by an internal mutex; Limit() is lock-free via atomic cache.
//
// Philosophy (The Ultimate Truth): We scale based on actual execution latency (time), not queue depth.
// Queue length is a subjective metric that varies per workload; a queue of 1,000 sleep tasks means nothing,
// while a queue of 10 cryptographic hashing tasks means total deadlock. Temporal latency is the
// objective reality of system saturation. AIMD breathes in response to this truth.
type AIMD struct {
	mu           sync.Mutex
	atomicLimit  atomic.Int64
	alpha        float64
	beta         float64
	limit        float64
	floor        float64
	ceil         float64
	target           float64 // target latency in nanoseconds (immutable after construction)
	lastDecreaseNano atomic.Int64
}

// NewAIMD creates an AIMD controller with the given parameters.
func NewAIMD(alpha, beta, targetLatencyNano, initialLimit, floor, ceil float64) *AIMD {
	if floor < 1 {
		floor = 1
	}
	if initialLimit < floor {
		initialLimit = floor
	}
	if ceil <= 0 {
		// Bind to a high physical limit to prevent runaway growth
		// under sustained ultra-high throughput.
		ceil = float64(runtime.GOMAXPROCS(0) * 2048)
	}
	if ceil > 0 && floor > ceil {
		floor = ceil
	}
	if initialLimit > ceil {
		initialLimit = ceil
	}
	a := &AIMD{
		alpha:  alpha,
		beta:   beta,
		limit:  initialLimit,
		floor:  floor,
		ceil:   ceil,
		target: targetLatencyNano,
	}
	a.atomicLimit.Store(int64(initialLimit))
	a.lastDecreaseNano.Store(NowNano())
	return a
}

// Increase probes for more capacity when execution latency is healthy.
// Uses direct additive increase (not alpha/limit) because we are driven by
// temporal ticks (~10ms), not per-ACK events; TCP-style normalization would
// cause catastrophic recovery stalling at high concurrency.
func (a *AIMD) Increase() {
	a.mu.Lock()
	defer a.mu.Unlock()

	step := a.alpha

	// Smooth deceleration as limit approaches ceiling to prevent oscillation.
	// A continuous proximity decay from 70% to 100% of ceil avoids the
	// jitter caused by a binary step-size switch at a single threshold.
	if a.ceil > 0 && a.limit > a.ceil*0.7 {
		proximity := (a.limit - a.ceil*0.7) / (a.ceil * 0.3) // [0, 1]
		step = a.alpha * (1.0 - proximity*0.9)               // decays to 10% of alpha
	}

	a.limit += step
	if a.ceil > 0 && a.limit > a.ceil {
		a.limit = a.ceil
	}
	a.atomicLimit.Store(int64(a.limit))
}

// Decrease sheds load when execution latency exceeds the target.
// The penalty is proportional to the severity of the overshoot.
func (a *AIMD) Decrease(latencyNano float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Cooldown prevents a single delayed metric scrape from triggering
	// cascading cuts. 
	// [Architectural Note]: The cooldown MUST be larger than the observed P99 tail latency.
	// In cloud environments, STW (Stop-The-World) GC pauses or network jitter can create 
	// 200ms+ latency spikes. If the cooldown relies only on the Target Latency (e.g. 5ms), 
	// a single 200ms pause would allow AIMD to sample and slash the limit 10+ times 
	// before the system unfreezes, causing an AIMD "Death Spiral".
	now := NowNano()
	baseCooldown := int64(time.Millisecond * 100)
	jitterProtection := int64(latencyNano * 2.0) // Shield against the *actual* spike magnitude
	if jitterProtection < int64(a.target*4.0) {
		jitterProtection = int64(a.target * 4.0)
	}
	
	cooldown := baseCooldown + jitterProtection
	if now-a.lastDecreaseNano.Load() < cooldown {
		return
	}
	a.lastDecreaseNano.Store(now)

	ratio := latencyNano / a.target
	effectiveBeta := a.beta
	if ratio > 1.0 {
		effectiveBeta = a.beta / ratio
		if effectiveBeta < 0.2 {
			effectiveBeta = 0.2 // prevent halving the limit too aggressively
		}
	}

	a.limit *= effectiveBeta
	if a.limit < a.floor {
		a.limit = a.floor
	}
	a.atomicLimit.Store(int64(a.limit))
}

// Observe feeds an execution latency sample and adjusts the limit accordingly.
func (a *AIMD) Observe(latencyNano float64) {
	if latencyNano > a.target {
		a.Decrease(latencyNano)
	} else {
		a.Increase()
	}
}

// Limit returns the current concurrency ceiling lock-free.
func (a *AIMD) Limit() int {
	v := a.atomicLimit.Load()
	if v < 1 {
		return 1
	}
	return int(v)
}
