// Package config loads the nvidiatemp YAML configuration, applies built-in
// defaults and validates it at startup.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"nvidiatemp/internal/curve"
)

// DefaultPath is the default configuration file location.
const DefaultPath = "/etc/nvidiatemp.yaml"

// Defaults (spec: configuration).
const (
	DefaultPollInterval    = 2 * time.Second
	DefaultDeadbandPct     = 5.0
	DefaultWritePause      = 5 * time.Second
	DefaultPowerThresholdW = 50.0
	// DefaultAutoDelay is the auto-cooldown: after the exit conditions first
	// hold, the fan is held at minimum speed this long before switching to
	// auto mode (cancelled if load returns in the meantime).
	DefaultAutoDelay = 5 * time.Minute
)

// DefaultCurve is the built-in default curve: 50→30, 60→40, 65→45, 70→55,
// 75→60, 80→70.
var DefaultCurve = curve.Curve{
	{Temp: 50, Speed: 30},
	{Temp: 60, Speed: 40},
	{Temp: 65, Speed: 45},
	{Temp: 70, Speed: 55},
	{Temp: 75, Speed: 60},
	{Temp: 80, Speed: 70},
}

// Config is the effective configuration with all defaults applied.
type Config struct {
	PollInterval    time.Duration
	DeadbandPct     float64 // %
	WritePause      time.Duration
	PowerThresholdW float64 // W
	AutoDelay       time.Duration
	Curve           curve.Curve
	PerIndex        map[int]curve.Curve // optional per-GPU-index curve overrides
}

// duration accepts either a Go duration string ("2s", "1m30s") or a bare
// number of seconds (2).
type duration struct{ time.Duration }

func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	var n float64
	if err := node.Decode(&n); err == nil {
		d.Duration = time.Duration(n * float64(time.Second))
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("invalid duration %q: use a Go duration (\"2s\") or seconds as a number", node.Value)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %v", s, err)
	}
	d.Duration = parsed
	return nil
}

// rawPoint mirrors one «температура → скорость» pair in the YAML file.
type rawPoint struct {
	Temp  float64 `yaml:"temp"`
	Speed float64 `yaml:"speed"`
}

func toCurve(ps []rawPoint) curve.Curve {
	c := make(curve.Curve, len(ps))
	for i, p := range ps {
		c[i] = curve.Point{Temp: p.Temp, Speed: p.Speed}
	}
	return c
}

// rawConfig is the on-disk shape; pointer fields distinguish «absent»
// (apply default) from an explicit value.
type rawConfig struct {
	PollInterval   *duration          `yaml:"poll_interval"`
	Deadband       *float64           `yaml:"deadband"`
	WritePause     *duration          `yaml:"write_pause"`
	PowerThreshold *float64           `yaml:"power_threshold"`
	AutoDelay      *duration          `yaml:"auto_delay"`
	Curve          *[]rawPoint        `yaml:"curve"`
	GPUCurves      map[int][]rawPoint `yaml:"gpu_curves"`
}

// Default returns the built-in configuration (used when the file is absent).
func Default() *Config {
	c := make(curve.Curve, len(DefaultCurve))
	copy(c, DefaultCurve)
	return &Config{
		PollInterval:    DefaultPollInterval,
		DeadbandPct:     DefaultDeadbandPct,
		WritePause:      DefaultWritePause,
		PowerThresholdW: DefaultPowerThresholdW,
		AutoDelay:       DefaultAutoDelay,
		Curve:           c,
		PerIndex:        map[int]curve.Curve{},
	}
}

// Load reads the configuration from path and applies built-in defaults for
// missing fields. A missing file yields the pure built-in defaults; an
// empty file does the same. A malformed file is an error.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if raw.PollInterval != nil {
		cfg.PollInterval = raw.PollInterval.Duration
	}
	if raw.Deadband != nil {
		cfg.DeadbandPct = *raw.Deadband
	}
	if raw.WritePause != nil {
		cfg.WritePause = raw.WritePause.Duration
	}
	if raw.PowerThreshold != nil {
		cfg.PowerThresholdW = *raw.PowerThreshold
	}
	if raw.AutoDelay != nil {
		cfg.AutoDelay = raw.AutoDelay.Duration
	}
	if raw.Curve != nil {
		cfg.Curve = toCurve(*raw.Curve)
	}
	for idx, ps := range raw.GPUCurves {
		cfg.PerIndex[idx] = toCurve(ps)
	}
	return cfg, nil
}

// Validate checks the effective configuration: every curve (common and
// per-index) must have strictly increasing temperatures with speeds in
// [30, 100], and the power threshold must be positive.
func (c *Config) Validate() error {
	if c.PowerThresholdW <= 0 {
		return fmt.Errorf("power threshold must be positive, got %g W", c.PowerThresholdW)
	}
	if err := c.Curve.Validate(); err != nil {
		return fmt.Errorf("common curve: %w", err)
	}
	for idx, crv := range c.PerIndex {
		if err := crv.Validate(); err != nil {
			return fmt.Errorf("curve for GPU %d: %w", idx, err)
		}
	}
	return nil
}

// CurveFor returns the effective curve for a GPU index: the per-index
// override if present, otherwise the common curve.
func (c *Config) CurveFor(index int) curve.Curve {
	if crv, ok := c.PerIndex[index]; ok && len(crv) > 0 {
		return crv
	}
	return c.Curve
}
