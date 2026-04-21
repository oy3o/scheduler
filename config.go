package scheduler

import "time"

// Config holds all tunable parameters for a Gatekeeper instance.
// Philosophy (Structural Control): Parameters here define the physical and temporal bounds
// of the scheduling universe. There are no "magic numbers" hidden in the implementation;
// every limit that dictates system survival must be explicitly modeled.
type Config struct {
	InitialLimit        int
	MinConcurrency      int
	MaxConcurrency      int
	AIMDAlpha           float64
	AIMDBeta            float64
	TargetLatency       time.Duration
	MinAIMDSamples      int
	ZombieTimeout       time.Duration
	MaxZombies          int
	CallbackTimeout     time.Duration
	DeathRattleTimeout  time.Duration
	StrictLivelockPanic bool
	OnError             func(task Task, err error)
	OnPanic             func(task Task, panicValue any)
}

// DefaultConfig returns a production-safe default configuration.
func DefaultConfig() Config {
	return Config{
		InitialLimit:        64,
		MinConcurrency:      4,
		MaxConcurrency:      1024,
		AIMDAlpha:           1,
		AIMDBeta:            0.8,
		TargetLatency:       time.Duration(Tau),
		MinAIMDSamples:      10,
		ZombieTimeout:       30 * time.Second,
		MaxZombies:          100_000,
		CallbackTimeout:     50 * time.Millisecond,
		DeathRattleTimeout:  5 * time.Second,
		StrictLivelockPanic: false, // Default to telemetry only
	}
}
