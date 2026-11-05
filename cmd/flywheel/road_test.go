package main

import (
	"math"
	"testing"
	"time"
)

// steadySpeed solves power = (aero + rolling) * v for v by bisection.
func steadySpeed(power, riderKG float64) float64 {
	kg := riderKG + bikeKG
	lo, hi := 0.0, 50.0
	for range 100 {
		v := (lo + hi) / 2
		if (0.5*airDensity*cdA*v*v+crr*kg*gravity)*v < power {
			lo = v
		} else {
			hi = v
		}
	}
	return lo
}

func TestRoadStepSettles(t *testing.T) {
	for _, tt := range []struct{ power, minKMH, maxKMH float64 }{
		{100, 24, 29},
		{200, 32, 37},
		{300, 37, 42},
	} {
		v := 0.0
		for range 600 {
			v = roadStep(v, tt.power, 75, time.Second)
		}
		want := steadySpeed(tt.power, 75)
		if math.Abs(v-want) > 0.01 {
			t.Errorf("%v W settles at %.3f m/s, want %.3f", tt.power, v, want)
		}
		if kmh := v * 3.6; kmh < tt.minKMH || kmh > tt.maxKMH {
			t.Errorf("%v W gives %.1f km/h, want %v-%v on a flat road", tt.power, kmh, tt.minKMH, tt.maxKMH)
		}
	}
}

func TestRoadStepCoasts(t *testing.T) {
	if v := roadStep(0, 0, 75, time.Second); v != 0 {
		t.Errorf("standing still at 0 W gives %v m/s", v)
	}
	v := 40 / 3.6
	for i := range 15 * 60 {
		next := roadStep(v, 0, 75, time.Second)
		if next > v || next < 0 {
			t.Fatalf("coasting from %v m/s gave %v m/s after %d s", v, next, i)
		}
		v = next
	}
	if v != 0 {
		t.Errorf("still rolling at %v m/s after 15 min of coasting", v)
	}
}
