package controller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

// ErrSustainedPollFailure signals that the consecutive-failure threshold was
// reached: the daemon has restored auto mode on all GPUs and must exit with
// a non-zero code (systemd then restarts it).
var ErrSustainedPollFailure = errors.New("sustained poll failure")

// defaultPollInterval is a safety fallback when PollInterval is unset; main
// always sets it from the configuration.
const defaultPollInterval = 2 * time.Second

// Daemon is the control-loop orchestration layer: it polls the Reader, runs
// the pure Tick decision per GPU, applies the resulting action through the
// FanSetter and tracks internal mode/last-write state (design D3). It is
// built on interfaces so unit tests run against fakes without a GPU.
type Daemon struct {
	Params       Params
	PollInterval time.Duration
	Reader       gpu.Reader
	Setter       gpu.FanSetter
	Modes        gpu.ModeReader              // optional; used once at startup to seed state
	CurveFor     func(index int) curve.Curve // per-GPU effective curve; falls back to Params.Curve
	Failures     *FailureTracker
	Log          *slog.Logger
	Clock        func() time.Time // injectable for tests; defaults to time.Now
}

// gpuState is the per-GPU internal state kept between ticks.
type gpuState struct {
	mode       gpu.Mode
	lastWrite  time.Time
	idleSince  time.Time // auto-cooldown start; zero means «not cooling down»
	quietSince time.Time // quiet-hold start; zero means «not in a quiet hold»
}

// NewDaemon returns a Daemon with default failure tracking, logging and clock.
func NewDaemon(p Params, r gpu.Reader, s gpu.FanSetter) *Daemon {
	return &Daemon{
		Params:   p,
		Reader:   r,
		Setter:   s,
		Failures: NewFailureTracker(),
		Log:      slog.Default(),
		Clock:    time.Now,
	}
}

// Run executes the control loop until ctx is cancelled (clean shutdown) or a
// sustained poll failure occurs. It polls immediately at startup and then on
// every interval; every failed poll feeds the consecutive-failure policy. In
// every case it restores auto mode on all managed GPUs before returning
// (spec: «Восстановление авто-режима при остановке»). It returns nil on
// clean shutdown, ErrSustainedPollFailure when the failure threshold is
// reached, or gpu.ErrNoGPUs immediately when no GPU is visible.
func (d *Daemon) Run(ctx context.Context) error {
	states := map[int]*gpuState{}
	if d.Modes != nil {
		// Seed state from the hardware once at startup (design D3): this
		// picks up a card left in manual mode by a previous run.
		if modes, merr := d.Modes.ReadModes(); merr == nil {
			for idx, m := range modes {
				states[idx] = &gpuState{mode: m}
			}
		} else {
			d.log().Warn("could not read initial fan modes", "error", merr)
		}
	}

	defer d.restoreAll(states)

	interval := d.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		now := d.clock()
		infos, err := d.Reader.Read()
		if err != nil {
			if errors.Is(err, gpu.ErrNoGPUs) {
				return err // immediate, clear no-GPU error
			}
			d.log().Error("poll failed", "error", err)
			if d.Failures.Record(false) {
				return ErrSustainedPollFailure // restoreAll runs via defer
			}
		} else {
			d.Failures.Record(true)
			d.process(infos, states, now)
		}

		select {
		case <-ctx.Done():
			return nil // clean shutdown; restoreAll runs via defer
		case <-ticker.C:
		}
	}
}

// process runs one tick over all polled GPUs.
func (d *Daemon) process(infos []gpu.Info, states map[int]*gpuState, now time.Time) {
	for _, info := range infos {
		st, ok := states[info.Index]
		if !ok {
			st = &gpuState{} // unknown mode: assume auto
			states[info.Index] = st
		}
		state := State{
			TempC:       info.TempC,
			PowerW:      info.PowerW,
			FanSpeedPct: info.FanSpeedPct,
			Mode:        st.mode,
			LastWrite:   st.lastWrite,
			IdleSince:   st.idleSince,
			QuietSince:  st.quietSince,
		}
		params := d.Params
		if d.CurveFor != nil {
			params.Curve = d.CurveFor(info.Index)
		}
		action, idleSince, quietSince := Tick(params, state, now)
		switch {
		case st.idleSince.IsZero() && !idleSince.IsZero():
			d.log().Info("auto-cooldown started", "gpu", info.Index,
				"delay", d.Params.AutoDelay.String(), "speed", curve.MinSpeed)
		case !st.idleSince.IsZero() && idleSince.IsZero() && action.Kind != ActionSwitchAuto:
			d.log().Info("auto-cooldown cancelled (load returned)", "gpu", info.Index)
		case st.quietSince.IsZero() && !quietSince.IsZero():
			d.log().Info("quiet hold started", "gpu", info.Index,
				"fan", info.FanSpeedPct, "hold", d.Params.QuietHold.String())
		case !st.quietSince.IsZero() && quietSince.IsZero():
			d.log().Info("quiet hold ended", "gpu", info.Index)
		}
		st.idleSince = idleSince
		st.quietSince = quietSince
		d.log().Debug("tick", "gpu", info.Index, "temp", info.TempC, "power", info.PowerW,
			"fan", info.FanSpeedPct, "mode", state.Mode, "action", action.Kind)
		if err := d.apply(action, info.Index, st, now); err != nil {
			d.log().Error("apply action failed", "gpu", info.Index, "action", action.Kind, "error", err)
		}
	}
}

// apply executes an action and updates the internal state on success.
func (d *Daemon) apply(action Action, index int, st *gpuState, now time.Time) error {
	switch action.Kind {
	case ActionNoop:
		return nil
	case ActionSwitchManual:
		if err := d.Setter.SetManual(index, action.Speed); err != nil {
			return err
		}
		st.mode = gpu.ModeManual
		st.lastWrite = now
		d.log().Info("switched to manual", "gpu", index, "speed", action.Speed)
	case ActionUpdateSpeed:
		// Already manual; SetManual re-asserts mode+speed (idempotent, D4).
		if err := d.Setter.SetManual(index, action.Speed); err != nil {
			return err
		}
		st.lastWrite = now
		d.log().Info("updated fan speed", "gpu", index, "speed", action.Speed)
	case ActionSwitchAuto:
		if err := d.Setter.SetAuto(index); err != nil {
			return err
		}
		st.mode = gpu.ModeAuto
		st.lastWrite = now
		d.log().Info("switched to auto", "gpu", index)
	}
	return nil
}

// restoreAll returns every managed GPU to auto mode (best effort).
func (d *Daemon) restoreAll(states map[int]*gpuState) {
	for idx, st := range states {
		if err := d.Setter.SetAuto(idx); err != nil {
			d.log().Error("failed to restore auto mode", "gpu", idx, "error", err)
		} else {
			st.mode = gpu.ModeAuto
			d.log().Info("restored auto mode", "gpu", idx)
		}
	}
}

func (d *Daemon) clock() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

func (d *Daemon) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
