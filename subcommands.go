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
	daemon := controller.NewDaemon(controller.Params{
		PowerThresholdW: cfg.PowerThresholdW,
		DeadbandPct:     cfg.DeadbandPct,
		WritePause:      cfg.WritePause,
		AutoDelay:       cfg.AutoDelay,
		Curve:           cfg.Curve,
	}, gpu.NewNvidiaReader(), gpu.NewNvidiaSetter())
	daemon.PollInterval = cfg.PollInterval
	daemon.Modes = gpu.NewNvidiaModeReader()
	daemon.CurveFor = cfg.CurveFor
	daemon.Log = log

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "config", *cfgPath, "poll_interval", cfg.PollInterval.String(),
		"power_threshold_w", cfg.PowerThresholdW, "deadband_pct", cfg.DeadbandPct,
		"write_pause", cfg.WritePause.String(), "auto_delay", cfg.AutoDelay.String())
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

	reader := gpu.NewNvidiaReader()
	modeReader := gpu.NewNvidiaModeReader()

	infos, err := reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}
	modes, err := modeReader.ReadModes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}

	for _, info := range infos {
		mode := gpu.ModeAuto
		if m, ok := modes[info.Index]; ok {
			mode = m
		}
		target := cfg.CurveFor(info.Index).SpeedAt(info.TempC)
		fmt.Printf("GPU %d: %s\n", info.Index, info.Name)
		fmt.Printf("  temperature:   %.1f°C\n", info.TempC)
		fmt.Printf("  fan speed:     %.0f%%\n", info.FanSpeedPct)
		fmt.Printf("  mode:          %s\n", mode)
		fmt.Printf("  target (curve): %.0f%%\n", target)
		fmt.Printf("  power:         %.1f W\n", info.PowerW)
	}
	return 0
}

// cmdSet switches all visible GPUs to manual mode at the given speed without
// starting the control loop. A value outside [30, 100] is rejected before
// any GPU state is touched. The keyword "auto" returns all GPUs to driver
// control instead.
func cmdSet(args []string) int {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "nvidiatemp: usage: nvidiatemp set <pct|auto>")
		return 2
	}

	auto := strings.EqualFold(fs.Arg(0), "auto")
	var pct float64
	if !auto {
		var err error
		pct, err = strconv.ParseFloat(fs.Arg(0), 64)
		if err != nil || pct < curve.MinSpeed || pct > curve.MaxSpeed {
			fmt.Fprintf(os.Stderr, "nvidiatemp: speed must be a number in [%.0f, %.0f] (hardware minimum is %.0f%%)\n",
				curve.MinSpeed, curve.MaxSpeed, curve.MinSpeed)
			return 1
		}
	}

	reader := gpu.NewNvidiaReader()
	setter := gpu.NewNvidiaSetter()

	infos, err := reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nvidiatemp: %v\n", err)
		return 1
	}

	failed := false
	for _, info := range infos {
		if auto {
			if err := setter.SetAuto(info.Index); err != nil {
				fmt.Fprintf(os.Stderr, "nvidiatemp: GPU %d: %v\n", info.Index, err)
				failed = true
				continue
			}
			fmt.Printf("GPU %d: auto mode\n", info.Index)
		} else {
			if err := setter.SetManual(info.Index, pct); err != nil {
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
