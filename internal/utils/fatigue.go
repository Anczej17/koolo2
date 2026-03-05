package utils

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

var (
	sessionStart   time.Time
	sessionStartMu sync.Once
)

func init() {
	sessionStartMu.Do(func() {
		sessionStart = time.Now()
	})
}

// ResetSessionClock resets the fatigue session timer (call at run start).
func ResetSessionClock() {
	sessionStart = time.Now()
}

// DriftFatigue returns a fatigue multiplier (1.0 → ~1.25) that increases
// logarithmically with session duration, simulating human motor fatigue.
func DriftFatigue() float64 {
	elapsed := time.Since(sessionStart).Minutes()
	if elapsed < 0 {
		elapsed = 0
	}
	return 1.0 + 0.05*math.Log1p(elapsed)
}

// GammaDurationMs returns a gamma-distributed duration with the given mean and shape.
func GammaDurationMs(meanMs float64, shape float64) time.Duration {
	if shape <= 0 {
		shape = 1.0
	}
	if meanMs <= 0 {
		return 0
	}
	scale := meanMs / shape
	sample := gammaVariate(shape, scale)
	if sample < 1 {
		sample = 1
	}
	return time.Duration(sample) * time.Millisecond
}

func gammaVariate(shape, scale float64) float64 {
	if shape < 1.0 {
		u := rand.Float64()
		if u == 0 {
			u = 1e-10
		}
		return gammaVariate(shape+1, scale) * math.Pow(u, 1.0/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		x := rand.NormFloat64()
		v := 1.0 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rand.Float64()
		if u < 1.0-0.0331*(x*x)*(x*x) {
			return d * v * scale
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v * scale
		}
	}
}
