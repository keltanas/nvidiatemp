// Package curve implements the pure temperature → fan speed function:
// piecewise-linear interpolation over curve points with clamping below
// the first point and above the last point.
package curve

import (
	"errors"
	"fmt"
)

// Point is one «температура (°C) → скорость (%)» pair of the curve.
type Point struct {
	Temp  float64 // °C
	Speed float64 // %
}

// Curve is an ordered list of points with strictly increasing temperatures.
type Curve []Point

// SpeedAt returns the target fan speed (%) for temp: piecewise-linear
// interpolation between surrounding points; below the first point the
// first point's speed, above the last point the last point's speed.
// The curve must be non-empty (guaranteed by Validate before use).
func (c Curve) SpeedAt(temp float64) float64 {
	if len(c) == 0 {
		panic("curve: SpeedAt on empty curve")
	}
	if temp <= c[0].Temp {
		return c[0].Speed
	}
	last := c[len(c)-1]
	if temp >= last.Temp {
		return last.Speed
	}
	for i := 1; i < len(c); i++ {
		if temp <= c[i].Temp {
			a, b := c[i-1], c[i]
			frac := (temp - a.Temp) / (b.Temp - a.Temp)
			return a.Speed + frac*(b.Speed-a.Speed)
		}
	}
	return last.Speed // unreachable for a sorted curve
}

// LowerBound returns the temperature of the first point — the lower bound
// of the curve and the exit-temperature condition for manual mode.
func (c Curve) LowerBound() float64 {
	if len(c) == 0 {
		panic("curve: LowerBound on empty curve")
	}
	return c[0].Temp
}

// MinSpeed and MaxSpeed bound every curve speed: the card's hardware
// minimum for manual mode is 30%.
const (
	MinSpeed = 30.0
	MaxSpeed = 100.0
)

// Validate reports an error if the curve is empty, temperatures are not
// strictly increasing, or any speed is outside [30, 100].
func (c Curve) Validate() error {
	if len(c) == 0 {
		return errors.New("curve: no points")
	}
	for i, p := range c {
		if p.Speed < MinSpeed || p.Speed > MaxSpeed {
			return fmt.Errorf("curve: point %d (%.1f°C → %.1f%%): speed outside [%.0f, %.0f]",
				i, p.Temp, p.Speed, MinSpeed, MaxSpeed)
		}
		if i > 0 && p.Temp <= c[i-1].Temp {
			return fmt.Errorf("curve: point %d (%.1f°C): temperatures must strictly increase (previous %.1f°C)",
				i, p.Temp, c[i-1].Temp)
		}
	}
	return nil
}
