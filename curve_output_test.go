package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nvidiatemp/internal/config"
)

// Spec scenario «Вывод эффективной кривой»: the output reflects the test
// config's contents with defaults applied where fields are absent.
func TestCurveOutputMatchesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	content := `
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
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	out := curveOutput(cfg)
	for _, want := range []string{
		"poll interval:    3s",
		"deadband:         7%",
		"write pause:      10s",
		"power threshold:  62.5 W",
		"auto delay:       2m0s",
		"backend:          auto",
		"default curve:",
		"45°C → 30%",
		"85°C → 90%",
		"GPU 1 curve:",
		"50°C → 40%",
		"75°C → 80%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// Without a config file the output shows the built-in defaults.
func TestCurveOutputDefaults(t *testing.T) {
	out := curveOutput(config.Default())
	for _, want := range []string{
		"poll interval:    2s",
		"deadband:         5%",
		"write pause:      5s",
		"power threshold:  50 W",
		"auto delay:       5m0s",
		"backend:          auto",
		"default curve:",
		"50°C → 30%",
		"60°C → 40%",
		"65°C → 45%",
		"70°C → 55%",
		"75°C → 60%",
		"80°C → 70%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GPU ") {
		t.Errorf("no per-index curves expected in defaults:\n%s", out)
	}
}
