package scheduler

import (
	"math/rand/v2"
	"testing"
)

func BenchmarkPercentileNoFrac(b *testing.B) {
	data := make([]float64, 1001)
	for i := range data {
		data[i] = rand.Float64()
	}
	d := make([]float64, 1001)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(d, data)
		sortedPercentileMut(d, 0.5)
	}
}
