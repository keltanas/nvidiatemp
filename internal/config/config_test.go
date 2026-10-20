package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Errorf("poll interval = %v, want %v", cfg.PollInterval, DefaultPollInterval)
	}
	if cfg.DeadbandPct != DefaultDeadbandPct {
		t.Errorf("deadband = %v, want %v", cfg.DeadbandPct, DefaultDeadbandPct)
	}
	if cfg.WritePause != DefaultWritePause {
		t.Errorf("write pause = %v, want %v", cfg.WritePause, DefaultWritePause)
	}
	if cfg.PowerThresholdW != DefaultPowerThresholdW {
		t.Errorf("power threshold = %v, want %v", cfg.PowerThresholdW, DefaultPowerThresholdW)
	}
	if cfg.AutoDelay != DefaultAutoDelay {
		t.Errorf("auto delay = %v, want %v", cfg.AutoDelay, DefaultAutoDelay)
	}
	if cfg.QuietThresholdPct != DefaultQuietThresholdPct {
		t.Errorf("quiet threshold = %v, want %v", cfg.QuietThresholdPct, DefaultQuietThresholdPct)
	}
	if cfg.QuietHold != DefaultQuietHold {
		t.Errorf("quiet hold = %v, want %v", cfg.QuietHold, DefaultQuietHold)
	}
	if cfg.QuietTempC != DefaultQuietTempC {
		t.Errorf("quiet temp = %v, want %v", cfg.QuietTempC, DefaultQuietTempC)
	}
	if cfg.QuietDelay != DefaultQuietDelay {
		t.Errorf("quiet delay = %v, want %v", cfg.QuietDelay, DefaultQuietDelay)
	}
	if cfg.Backend != gpu.BackendAuto {
		t.Errorf("backend = %q, want %q", cfg.Backend, gpu.BackendAuto)
	}
	if len(cfg.Curve) != len(DefaultCurve) {
		t.Fatalf("curve length = %d, want %d", len(cfg.Curve), len(DefaultCurve))
	}
	for i := range DefaultCurve {
		if cfg.Curve[i] != DefaultCurve[i] {
			t.Errorf("curve[%d] = %+v, want %+v", i, cfg.Curve[i], DefaultCurve[i])
		}
	}
}

// Spec scenario «Частичная конфигурация»: only the curve is set, the rest
// takes defaults.
func TestLoadPartialConfigCurveOnly(t *testing.T) {
	path := writeTempConfig(t, `
curve:
  - { temp: 55, speed: 35 }
  - { temp: 70, speed: 60 }
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := curve.Curve{{Temp: 55, Speed: 35}, {Temp: 70, Speed: 60}}
	if len(cfg.Curve) != len(want) {
		t.Fatalf("curve length = %d, want %d", len(cfg.Curve), len(want))
	}
	for i := range want {
		if cfg.Curve[i] != want[i] {
			t.Errorf("curve[%d] = %+v, want %+v", i, cfg.Curve[i], want[i])
		}
	}
	if cfg.PollInterval != DefaultPollInterval || cfg.DeadbandPct != DefaultDeadbandPct ||
		cfg.WritePause != DefaultWritePause || cfg.PowerThresholdW != DefaultPowerThresholdW {
		t.Errorf("unset fields must keep defaults, got %+v", cfg)
	}
}

// Spec scenario «Пользовательский путь»: full config read from -c path.
func TestLoadFullConfigFromCustomPath(t *testing.T) {
	path := writeTempConfig(t, `
poll_interval: 3s
deadband: 7
write_pause: 10
power_threshold: 62.5
auto_delay: 2m
curve:
  - { temp: 45, speed: 30 }
  - { temp: 85, speed: 90 }
gpu_curves:
  1:
    - { temp: 50, speed: 40 }
    - { temp: 75, speed: 80 }
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PollInterval != 3*time.Second {
		t.Errorf("poll interval = %v, want 3s", cfg.PollInterval)
	}
	if cfg.DeadbandPct != 7 {
		t.Errorf("deadband = %v, want 7", cfg.DeadbandPct)
	}
	if cfg.WritePause != 10*time.Second { // bare number = seconds
		t.Errorf("write pause = %v, want 10s", cfg.WritePause)
	}
	if cfg.PowerThresholdW != 62.5 {
		t.Errorf("power threshold = %v, want 62.5", cfg.PowerThresholdW)
	}
	if cfg.AutoDelay != 2*time.Minute {
		t.Errorf("auto delay = %v, want 2m", cfg.AutoDelay)
	}
	if len(cfg.Curve) != 2 || cfg.Curve[1].Temp != 85 || cfg.Curve[1].Speed != 90 {
		t.Errorf("curve = %+v, want [(45,30) (85,90)]", cfg.Curve)
	}
	idx1, ok := cfg.PerIndex[1]
	if !ok || len(idx1) != 2 || idx1[0] != (curve.Point{Temp: 50, Speed: 40}) {
		t.Errorf("per-index curve for GPU 1 = %+v", idx1)
	}
}

func TestLoadEmptyFileUsesDefaults(t *testing.T) {
	path := writeTempConfig(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("empty file must not be an error: %v", err)
	}
	def := Default()
	if cfg.PollInterval != def.PollInterval || cfg.PowerThresholdW != def.PowerThresholdW ||
		len(cfg.Curve) != len(def.Curve) {
		t.Errorf("empty file must yield defaults, got %+v", cfg)
	}
}

func TestLoadMalformedYAMLIsError(t *testing.T) {
	path := writeTempConfig(t, "curve: [not a list of points\n  broken: {")
	if _, err := Load(path); err == nil {
		t.Error("malformed YAML must be an error")
	}
}

// The quiet hold (catch a driver fan spin-up or a sustained idle temperature)
// is configurable; quiet_hold: 0 disables it.
func TestLoadQuietHold(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, "quiet_threshold: 45\nquiet_hold: 90\nquiet_temp: 55\nquiet_delay: 30\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.QuietThresholdPct != 45 {
		t.Errorf("quiet threshold = %v, want 45", cfg.QuietThresholdPct)
	}
	if cfg.QuietHold != 90*time.Second {
		t.Errorf("quiet hold = %v, want 90s", cfg.QuietHold)
	}
	if cfg.QuietTempC != 55 {
		t.Errorf("quiet temp = %v, want 55", cfg.QuietTempC)
	}
	if cfg.QuietDelay != 30*time.Second {
		t.Errorf("quiet delay = %v, want 30s", cfg.QuietDelay)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}

	off, err := Load(writeTempConfig(t, "quiet_hold: 0\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if off.QuietHold != 0 {
		t.Errorf("quiet hold = %v, want 0 (disabled)", off.QuietHold)
	}
}

// The fan-control backend can be pinned in the configuration file.
func TestLoadBackend(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, "backend: nvml\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Backend != gpu.BackendNVML {
		t.Errorf("backend = %q, want nvml", cfg.Backend)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// An unknown backend is rejected before any GPU access.
func TestLoadUnknownBackendIsError(t *testing.T) {
	if _, err := Load(writeTempConfig(t, "backend: nvlm\n")); err == nil {
		t.Error("unknown backend must fail at load")
	}
}

func TestValidateRejectsUnknownBackend(t *testing.T) {
	cfg := Default()
	cfg.Backend = gpu.BackendKind("bogus")
	if err := cfg.Validate(); err == nil {
		t.Error("Validate must reject an unknown backend")
	}
}

// Spec scenario «Переопределение на индекс»: GPU 1 uses its own curve,
// the rest use the common one.
func TestCurveForPerIndexOverride(t *testing.T) {
	path := writeTempConfig(t, `
curve:
  - { temp: 50, speed: 30 }
  - { temp: 80, speed: 70 }
gpu_curves:
  1:
    - { temp: 55, speed: 35 }
    - { temp: 85, speed: 80 }
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gpu1 := cfg.CurveFor(1)
	if len(gpu1) != 2 || gpu1[0] != (curve.Point{Temp: 55, Speed: 35}) {
		t.Errorf("GPU 1 curve = %+v, want its own override", gpu1)
	}
	gpu0 := cfg.CurveFor(0)
	if len(gpu0) != 2 || gpu0[0] != (curve.Point{Temp: 50, Speed: 30}) {
		t.Errorf("GPU 0 curve = %+v, want the common curve", gpu0)
	}
}
