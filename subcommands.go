package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"nvidiatemp/internal/config"
	"nvidiatemp/internal/controller"
	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

// newLogger builds the stderr slog logger (design D6): Info by default,
// Debug when -v is given.
func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// loadConfig loads and validates the configuration from -c. On error it
// prints a message to stderr and returns exit code 1 — before any GPU access
// or control loop starts (spec: configuration, «Валидация»).
func loadConfig(name string, args []string) (cfg *config.Config, rest []string, code int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfgPath := fs.String("c", config.DefaultPath, "configuration file")
	if err := fs.Parse(args); err != nil {
		return nil, nil, 2 // the flag package already printed the error
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return nil, nil, 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return nil, nil, 1
	}
	return cfg, fs.Args(), 0
}

// cmdRun starts the control loop as a daemon until SIGINT/SIGTERM or a
// sustained poll error. On every stop path the GPUs are returned to auto
// mode (handled inside controller.Daemon.Run).
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("c", config.DefaultPath, "configuration file")
	debug := fs.Bool("v", false, "verbose: log every tick (debug level)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "nvidiatemp: run takes no arguments\n")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}

	log := newLogger(*debug)
	backend, err := gpu.Open(cfg.Backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	defer backend.Close()
	if backend.Fallback != nil {
		log.Warn("NVML unavailable, falling back to nvidia-settings", "error", backend.Fallback)
	}

	daemon := controller.NewDaemon(controller.Params{
		PowerThresholdW:   cfg.PowerThresholdW,
		DeadbandPct:       cfg.DeadbandPct,
		WritePause:        cfg.WritePause,
		AutoDelay:         cfg.AutoDelay,
		QuietThresholdPct: cfg.QuietThresholdPct,
		QuietHold:         cfg.QuietHold,
		QuietTempC:        cfg.QuietTempC,
		QuietDelay:        cfg.QuietDelay,
		Curve:             cfg.Curve,
	}, backend.Reader, backend.Setter)
	daemon.PollInterval = cfg.PollInterval
	daemon.Modes = backend.Modes
	daemon.CurveFor = cfg.CurveFor
	daemon.Log = log

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "config", *cfgPath, "backend", backend.Kind,
		"poll_interval", cfg.PollInterval.String(),
		"power_threshold_w", cfg.PowerThresholdW, "deadband_pct", cfg.DeadbandPct,
		"write_pause", cfg.WritePause.String(), "auto_delay", cfg.AutoDelay.String(),
		"quiet_threshold_pct", cfg.QuietThresholdPct, "quiet_hold", cfg.QuietHold.String(),
		"quiet_temp_c", cfg.QuietTempC, "quiet_delay", cfg.QuietDelay.String())
	err = daemon.Run(ctx)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, controller.ErrSustainedPollFailure):
		log.Error("sustained poll failure; exiting for restart")
		return 1
	default:
		log.Error("run failed", "error", err)
		return 1
	}
}

// cmdStatus prints a one-shot per-GPU state: temperature, current fan speed,
// control mode, target speed from the curve and power.
func cmdStatus(args []string) int {
	cfg, rest, code := loadConfig("status", args)
	if code != 0 {
		return code
	}
	if len(rest) != 0 {
		fmt.Fprintf(os.Stderr, "nvidiatemp: status takes no arguments\n")
		return 2
	}

	backend, err := gpu.OpenReadOnly(cfg.Backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	defer backend.Close()

	infos, err := backend.Reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	modes, modeErr := backend.Modes.ReadModes()
	if modeErr != nil {
		// Keep printing temperature/power/fan: the control mode needs a
		// working write path (X display or NVML), the rest does not.
		fmt.Fprintf(os.Stderr, "nvidiatemp: fan control mode unavailable: %v\n", modeErr)
	}

	for _, info := range infos {
		modeText := "unknown"
		if m, ok := modes[info.Index]; ok {
			modeText = m.String()
		}
		target := cfg.CurveFor(info.Index).SpeedAt(info.TempC)
		fmt.Printf("GPU %d: %s\n", info.Index, info.Name)
		fmt.Printf("  temperature:   %.1f°C\n", info.TempC)
		fmt.Printf("  fan speed:     %.0f%%\n", info.FanSpeedPct)
		fmt.Printf("  mode:          %s\n", modeText)
		fmt.Printf("  target (curve): %.0f%%\n", target)
		fmt.Printf("  power:         %.1f W\n", info.PowerW)
	}
	if modeErr != nil {
		return 1
	}
	return 0
}

// cmdSet switches all visible GPUs to manual mode at the given speed without
// starting the control loop. A value outside [30, 100] is rejected before
// any GPU state is touched. The keyword "auto" returns all GPUs to driver
// control instead.
func cmdSet(args []string) int {
	cfg, rest, code := loadConfig("set", args)
	if code != 0 {
		return code
	}
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "nvidiatemp: usage: nvidiatemp set [-c PATH] <pct|auto>")
		return 2
	}

	auto := strings.EqualFold(rest[0], "auto")
	var pct float64
	if !auto {
		var err error
		pct, err = strconv.ParseFloat(rest[0], 64)
		if err != nil || pct < curve.MinSpeed || pct > curve.MaxSpeed {
			fmt.Fprintf(os.Stderr, "nvidiatemp: speed must be a number in [%.0f, %.0f] (hardware minimum is %.0f%%)\n",
				curve.MinSpeed, curve.MaxSpeed, curve.MinSpeed)
			return 1
		}
	}

	backend, err := gpu.Open(cfg.Backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	defer backend.Close()

	infos, err := backend.Reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}

	failed := false
	for _, info := range infos {
		if auto {
			if err := backend.Setter.SetAuto(info.Index); err != nil {
				fmt.Fprintf(os.Stderr, "nvidiatemp: GPU %d: %v\n", info.Index, err)
				failed = true
				continue
			}
			fmt.Printf("GPU %d: auto mode\n", info.Index)
		} else {
			if err := backend.Setter.SetManual(info.Index, pct); err != nil {
				fmt.Fprintf(os.Stderr, "nvidiatemp: GPU %d: %v\n", info.Index, err)
				failed = true
				continue
			}
			fmt.Printf("GPU %d: manual mode at %.0f%%\n", info.Index, pct)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// cmdCurve prints the effective curve (common + per-index overrides) and the
// control-loop parameters with configuration and defaults applied.
func cmdCurve(args []string) int {
	cfg, rest, code := loadConfig("curve", args)
	if code != 0 {
		return code
	}
	if len(rest) != 0 {
		fmt.Fprintf(os.Stderr, "nvidiatemp: curve takes no arguments\n")
		return 2
	}
	fmt.Print(curveOutput(cfg))
	return 0
}
