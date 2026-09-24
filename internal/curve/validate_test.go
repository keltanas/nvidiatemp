package curve

import "testing"

func TestValidateAcceptsValidCurve(t *testing.T) {
	c := Curve{
		{Temp: 50, Speed: 30},
		{Temp: 60, Speed: 40},
		{Temp: 80, Speed: 70},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("valid curve rejected: %v", err)
	}
}

func TestValidateRejectsUnorderedTemperatures(t *testing.T) {
	cases := []Curve{
		{{Temp: 60, Speed: 40}, {Temp: 50, Speed: 30}}, // decreasing
		{{Temp: 60, Speed: 40}, {Temp: 60, Speed: 45}}, // equal (not strictly increasing)
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: unordered temperatures accepted", i)
		}
	}
}

func TestValidateRejectsSpeedBelowMinimum(t *testing.T) {
	c := Curve{{Temp: 50, Speed: 20}, {Temp: 60, Speed: 40}}
	if err := c.Validate(); err == nil {
		t.Error("speed below 30 accepted")
	}
}

func TestValidateRejectsSpeedAboveMaximum(t *testing.T) {
	c := Curve{{Temp: 50, Speed: 30}, {Temp: 60, Speed: 101}}
	if err := c.Validate(); err == nil {
		t.Error("speed above 100 accepted")
	}
}

func TestValidateRejectsEmptyCurve(t *testing.T) {
	if err := (Curve{}).Validate(); err == nil {
		t.Error("empty curve accepted")
	}
}

func TestValidateBoundarySpeedsAccepted(t *testing.T) {
	c := Curve{{Temp: 50, Speed: 30}, {Temp: 60, Speed: 100}}
	if err := c.Validate(); err != nil {
		t.Errorf("boundary speeds rejected: %v", err)
	}
}
