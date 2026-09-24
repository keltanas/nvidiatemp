package curve

import (
	"math"
	"testing"
)

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Spec scenario «Линейная интерполяция»: 62°C with points (60→40) and
// (65→45) → 42%.
func TestSpeedAtInterpolation(t *testing.T) {
	c := Curve{{Temp: 60, Speed: 40}, {Temp: 65, Speed: 45}}
	assertClose(t, c.SpeedAt(62), 42)
}

// Interpolation on the default curve between non-adjacent-to-endpoints.
func TestSpeedAtInterpolationDefaultCurve(t *testing.T) {
	c := Curve{
		{Temp: 50, Speed: 30},
		{Temp: 60, Speed: 40},
		{Temp: 65, Speed: 45},
		{Temp: 70, Speed: 55},
		{Temp: 75, Speed: 60},
		{Temp: 80, Speed: 70},
	}
	assertClose(t, c.SpeedAt(67.5), 50) // midpoint of (65→45)-(70→55)
	assertClose(t, c.SpeedAt(55), 35)   // midpoint of (50→30)-(60→40)
}

// Exact hit on a point returns that point's speed.
func TestSpeedAtExactPoint(t *testing.T) {
	c := Curve{
		{Temp: 50, Speed: 30},
		{Temp: 60, Speed: 40},
		{Temp: 65, Speed: 45},
		{Temp: 70, Speed: 55},
	}
	assertClose(t, c.SpeedAt(50), 30) // first point
	assertClose(t, c.SpeedAt(60), 40) // interior point
	assertClose(t, c.SpeedAt(70), 55) // last point
}

// Spec scenario «Клампинг выше последней точки»: temp ≥ 80 with last point
// (80→70) → 70%.
func TestSpeedAtClampAbove(t *testing.T) {
	c := Curve{
		{Temp: 50, Speed: 30},
		{Temp: 60, Speed: 40},
		{Temp: 80, Speed: 70},
	}
	assertClose(t, c.SpeedAt(80), 70)
	assertClose(t, c.SpeedAt(95), 70)
	assertClose(t, c.SpeedAt(120), 70)
}

// Spec scenario «Клампинг ниже первой точки»: temp below the lower bound
// (50°C) with first point (50→30) → 30%.
func TestSpeedAtClampBelow(t *testing.T) {
	c := Curve{
		{Temp: 50, Speed: 30},
		{Temp: 60, Speed: 40},
	}
	assertClose(t, c.SpeedAt(49), 30)
	assertClose(t, c.SpeedAt(0), 30)
	assertClose(t, c.SpeedAt(-10), 30)
}

// A single-point curve is constant everywhere.
func TestSpeedAtSinglePoint(t *testing.T) {
	c := Curve{{Temp: 60, Speed: 42}}
	assertClose(t, c.SpeedAt(10), 42)
	assertClose(t, c.SpeedAt(60), 42)
	assertClose(t, c.SpeedAt(90), 42)
}

func TestLowerBound(t *testing.T) {
	c := Curve{{Temp: 50, Speed: 30}, {Temp: 80, Speed: 70}}
	assertClose(t, c.LowerBound(), 50)
}
