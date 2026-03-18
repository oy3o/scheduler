package scheduler

import (
	"math"
	"testing"
)

func TestWeight(t *testing.T) {
	// Test priority < 0
	if w := Weight(-1); w != weightTable[0] {
		t.Errorf("expected Weight(-1) to be %v, got %v", weightTable[0], w)
	}

	// Test priority > 2000
	if w := Weight(2001); w != weightTable[2000] {
		t.Errorf("expected Weight(2001) to be %v, got %v", weightTable[2000], w)
	}

	// Test normal priority
	expected := 1.0 + math.Log2(float64(10)+1.0)*PriorityScale
	if expected < 1e-9 {
		expected = 1e-9
	}
	if w := Weight(10); math.Abs(w-expected) > 1e-9 {
		t.Errorf("expected Weight(10) to be %v, got %v", expected, w)
	}
}

func TestEnergy(t *testing.T) {
	tests := []struct {
		name             string
		runtimeNano      float64
		weight           float64
		creationPressure float64
		yieldCount       int
		expected         float64
	}{
		{
			name:             "weight <= 0 defaults to 1e-9",
			runtimeNano:      100.0,
			weight:           0,
			creationPressure: 0,
			yieldCount:       0,
			expected:         1.0 / 1e-9, // pressure=0, yield=0
		},
		{
			name:             "creationPressure < 0 defaults to 0",
			runtimeNano:      100.0,
			weight:           1.0,
			creationPressure: -5.0,
			yieldCount:       0,
			expected:         1.0, // weight=1, pressureLog=0, yield=0
		},
		{
			name:             "creationPressure < 1024 uses table",
			runtimeNano:      100.0,
			weight:           1.0,
			creationPressure: 10.0,
			yieldCount:       0,
			expected:         1.0 + PenaltyGamma*Tau*creationPenTable[10], // yield=0
		},
		{
			name:             "creationPressure >= 1024 uses math.Log",
			runtimeNano:      100.0,
			weight:           1.0,
			creationPressure: 2000.0,
			yieldCount:       0,
			expected:         1.0 + PenaltyGamma*Tau*math.Log(2001.0), // yield=0
		},
		{
			name:             "yieldCount < 0 defaults to 0",
			runtimeNano:      100.0,
			weight:           1.0,
			creationPressure: 0,
			yieldCount:       -5,
			expected:         1.0, // pressure=0, yieldLog=0
		},
		{
			name:             "yieldCount > 100 capped at 100",
			runtimeNano:      100.0,
			weight:           1.0,
			creationPressure: 0,
			yieldCount:       105,
			expected:         1.0 - FeedbackBeta*Tau*yieldBonusTable[100], // pressure=0
		},
		{
			name:             "large runtimeNano",
			runtimeNano:      5000000.0,
			weight:           2.0,
			creationPressure: 10.0,
			yieldCount:       5,
			expected:         (5.0 / 2.0) + PenaltyGamma*Tau*creationPenTable[10] - FeedbackBeta*Tau*yieldBonusTable[5],
		},
		{
			name:             "normal case combination",
			runtimeNano:      1000.0,
			weight:           2.0,
			creationPressure: 10.0,
			yieldCount:       5,
			expected:         (1.0 / 2.0) + PenaltyGamma*Tau*creationPenTable[10] - FeedbackBeta*Tau*yieldBonusTable[5],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Energy(tc.runtimeNano, tc.weight, tc.creationPressure, tc.yieldCount)
			if math.Abs(got-tc.expected) > 1e-5 {
				t.Errorf("Energy(%v, %v, %v, %v) = %v, expected %v",
					tc.runtimeNano, tc.weight, tc.creationPressure, tc.yieldCount, got, tc.expected)
			}
		})
	}
}
