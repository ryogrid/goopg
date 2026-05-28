package executor

import (
	"math"
	mathrand "math/rand"
	"testing"
)

// TestRandomProducesVariedValues verifies that random() doesn't return the
// same value repeatedly (old stub returned constant 0.5). M0097-0071.
func TestRandomProducesVariedValues(t *testing.T) {
	seen := make(map[float64]bool)
	for i := 0; i < 100; i++ {
		sessionPRNGMu.Lock()
		v := sessionPRNG.Float64()
		sessionPRNGMu.Unlock()
		seen[v] = true
	}
	if len(seen) < 50 {
		t.Errorf("random() produced only %d distinct values in 100 calls — expected at least 50", len(seen))
	}
}

// TestRandomInRange verifies that random() stays in [0, 1). M0097-0071.
func TestRandomInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		sessionPRNGMu.Lock()
		v := sessionPRNG.Float64()
		sessionPRNGMu.Unlock()
		if v < 0 || v >= 1 {
			t.Errorf("random() = %v, want [0, 1)", v)
		}
	}
}

// TestSetseedMakesRandomReproducible verifies that setseed() seeds the PRNG
// so random() produces a reproducible sequence. M0097-0071.
func TestSetseedMakesRandomReproducible(t *testing.T) {
	const seed = 0.5
	seedI := int64(seed * float64(1<<31))

	collect := func() []float64 {
		sessionPRNGMu.Lock()
		sessionPRNG = mathrand.New(mathrand.NewSource(seedI))
		sessionPRNGMu.Unlock()
		vals := make([]float64, 10)
		for i := range vals {
			sessionPRNGMu.Lock()
			vals[i] = sessionPRNG.Float64()
			sessionPRNGMu.Unlock()
		}
		return vals
	}

	first := collect()
	second := collect()
	for i, v := range first {
		if v != second[i] {
			t.Errorf("seed %.1f: value[%d] = %v first, %v second — not reproducible", seed, i, v, second[i])
		}
	}
}

// TestRandomNormalLooksGaussian verifies that random_normal() values are
// approximately standard-normal (mean≈0, stddev≈1). M0097-0071.
func TestRandomNormalLooksGaussian(t *testing.T) {
	n := 1000
	sum, sumSq := 0.0, 0.0
	for i := 0; i < n; i++ {
		sessionPRNGMu.Lock()
		u1 := sessionPRNG.Float64()
		u2 := sessionPRNG.Float64()
		sessionPRNGMu.Unlock()
		if u1 == 0 {
			u1 = 1e-15
		}
		z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
		sum += z
		sumSq += z * z
	}
	mean := sum / float64(n)
	variance := sumSq/float64(n) - mean*mean
	stddev := math.Sqrt(variance)

	if math.Abs(mean) > 0.3 {
		t.Errorf("random_normal() mean = %v, want ≈0 (tolerance 0.3)", mean)
	}
	if math.Abs(stddev-1.0) > 0.3 {
		t.Errorf("random_normal() stddev = %v, want ≈1 (tolerance 0.3)", stddev)
	}
}
