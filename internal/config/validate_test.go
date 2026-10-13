package config

import (
	"strings"
	"testing"
	"time"

	"nvidiatemp/internal/curve"
)

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestValidateRejectsNonPositivePowerThreshold(t *testing.T) {
	for _, w := range []float64{0, -12.5} {
		cfg := Default()
		cfg.PowerThresholdW = w
		if err := cfg.Validate(); err == nil {
			t.Errorf("power threshold %v accepted", w)
		}
	}
}

func TestValidateRejectsBadQuietHold(t *testing.T) {
	cfg := Default()
	cfg.QuietHold = -time.Minute
	if err := cfg.Validate(); err == nil {
		t.Error("negative quiet hold accepted")
	}
	for _, pct := range []float64{0, -10, 101} {
		cfg := Default()
		cfg.QuietThresholdPct = pct
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "quiet threshold") {
			t.Errorf("quiet threshold %v accepted: %v", pct, err)
		}
	}
	// Disabled feature: the threshold is not checked.
	cfg = Default()
	cfg.QuietHold = 0
	cfg.QuietThresholdPct = 0
	if err := cfg.Validate(); err != nil {
		t.Errorf("disabled quiet hold must not validate the threshold: %v", err)
	}
}

func TestValidateRejectsBadCommonCurve(t *testing.T) {
	cfg := Default()
	cfg.Curve = curve.Curve{{Temp: 60, Speed: 40}, {Temp: 50, Speed: 30}} // unordered
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "common curve") {
		t.Errorf("unordered common curve not rejected: %v", err)
	}
}

func TestValidateRejectsBadPerIndexCurve(t *testing.T) {
	cfg := Default()
	cfg.PerIndex[1] = curve.Curve{{Temp: 50, Speed: 20}} // speed below minimum
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "GPU 1") {
		t.Errorf("bad per-index curve not rejected: %v", err)
	}
}
