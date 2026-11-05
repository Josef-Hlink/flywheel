package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The road model turns power into speed on a flat road without wind, as
// riding apps do. The trainer reports a speed too, but that's its flywheel:
// with one fixed gear it's cadence times a constant, whatever the power.
const (
	bikeKG     = 9.0   // added to the rider's weight
	cdA        = 0.32  // m², a road bike on the hoods
	crr        = 0.004 // smooth tarmac
	airDensity = 1.225 // kg/m³, sea level at 15 °C
	gravity    = 9.81
)

// roadStep advances speed v (m/s) for dt at the given power, for a rider of
// riderKG on the bike. It steps kinetic energy rather than speed, so it can
// start from a standstill, and coasts down when power stops.
func roadStep(v, power, riderKG float64, dt time.Duration) float64 {
	kg := riderKG + bikeKG
	drag := (0.5*airDensity*cdA*v*v + crr*kg*gravity) * v // W
	energy := 0.5*kg*v*v + (power-drag)*dt.Seconds()
	if energy <= 0 {
		return 0
	}
	return math.Sqrt(2 * energy / kg)
}

// weightCmd shows or sets the rider's weight for the road model.
func weightCmd(args []string) error {
	r, err := loadRider()
	if err != nil {
		return err
	}
	switch len(args) {
	case 0:
		if r.WeightKG == 0 {
			fmt.Printf("%g kg, the default (flywheel weight 72 sets yours)\n", r.weight())
		} else {
			fmt.Printf("%g kg\n", r.weight())
		}
		return nil
	case 1:
	default:
		return errors.New("usage: flywheel weight [kg]")
	}

	num := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(args[0])), "kg")
	kg, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || kg < 30 || kg > 250 {
		return fmt.Errorf("%q: give your weight in kg, like 72 or 72.5", args[0])
	}
	r.WeightKG = kg
	if err := saveRider(r); err != nil {
		return err
	}
	fmt.Printf("%g kg, for rides from your next connect on\n", kg)
	return nil
}
