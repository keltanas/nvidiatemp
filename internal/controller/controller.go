// Package controller holds the pure per-tick decision logic: from the
// current measurements, the current mode and the last write time it
// produces an action (design D3). No I/O lives here.
package controller

import (
	"math"
	"time"

	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

// Params are the control parameters for one tick.
type Params struct {
	PowerThresholdW   float64 // W; manual mode entry above this power
	DeadbandPct       float64 // %; minimum |target − current| to write
	WritePause        time.Duration
	AutoDelay         time.Duration // auto-cooldown before returning to the driver
	QuietThresholdPct float64       // %; auto-mode fan speed that triggers a quiet hold
	QuietHold         time.Duration // manual mode this long after a quiet trigger; 0 disables it
	QuietTempC        float64       // °C; idle temperature that arms the quiet hold
	QuietDelay        time.Duration // how long QuietTempC must hold before the quiet hold starts
	Curve             curve.Curve
}

// State is the per-GPU input of one tick decision.
type State struct {
	TempC       float64
	PowerW      float64
	FanSpeedPct float64 // current speed as measured from the hardware
	Mode        gpu.Mode
	LastWrite   time.Time // zero means «never written»
	IdleSince   time.Time // auto-cooldown start; zero means «not cooling down»
	QuietSince  time.Time // quiet-hold start; zero means «not in a quiet hold»
	// QuietTempSince is the start of the idle «temperature ≥ QuietTempC»
	// window. While a quiet hold is running a non-zero value marks that the
	// hold was started by the temperature trigger, not by the driver's fan.
	QuietTempSince time.Time
}

// ActionKind is the kind of action a tick decides.
type ActionKind int

const (
	ActionNoop         ActionKind = iota // nothing to do
	ActionSwitchManual                   // switch to manual + set speed
	ActionUpdateSpeed                    // already manual, update speed
	ActionSwitchAuto                     // return to driver control
)

// Action is the result of a tick decision. Speed is meaningful for
// ActionSwitchManual and ActionUpdateSpeed.
type Action struct {
	Kind  ActionKind
	Speed float64 // %
}

// writeAllowed reports whether a write is permitted given the last write
// time and the pause between writes.
func writeAllowed(lastWrite, now time.Time, pause time.Duration) bool {
	return lastWrite.IsZero() || now.Sub(lastWrite) >= pause
}

// Tick decides the action for one GPU from its current state and returns the
// auto-cooldown start, the quiet-hold start and the idle-temperature window
// start to track going forward (zero = not cooling down / not in a quiet hold
// / temperature below the trigger):
//
//   - auto → manual when power exceeds the threshold (speed from the curve);
//   - auto → manual for a quiet hold when the driver spins the fan up on its
//     own (fan ≥ QuietThresholdPct) while the card is idle and not hot: the
//     card is controlled by the curve for QuietHold, then returned to auto;
//   - auto → manual for a quiet hold when an idle card has been at or above
//     QuietTempC continuously for QuietDelay (the fan may stay low on the
//     driver's own): same hold, but the curve's speed applies while the card
//     stays that warm;
//   - in manual mode, when power is below the threshold AND temperature is
//     below the lower bound of the curve the GPU enters the auto-cooldown:
//     the fan is held at the minimum speed, and after AutoDelay without load
//     it returns to auto mode; any break of the idle conditions cancels the
//     cooldown and control follows the curve again;
//   - a quiet hold ends earlier if load arrives (and, for a fan-triggered
//     hold, if the card heats up); then the card just follows the ordinary
//     manual-mode rules;
//   - in manual mode outside the cooldown the speed is written only when
//     |target − current| is at least the deadband;
//   - every write (mode or speed) is subject to the pause between writes —
//     this also damps mode flapping (design D4).
func Tick(p Params, s State, now time.Time) (Action, time.Time, time.Time, time.Time) {
	target := p.Curve.SpeedAt(s.TempC) // clamped by the curve to [30, 100]

	switch s.Mode {
	case gpu.ModeAuto:
		// Track how long the idle card has stayed at/above the temperature
		// trigger; any load or cooling below it resets the window.
		quietTempSince := time.Time{}
		if p.QuietHold > 0 && s.PowerW < p.PowerThresholdW && s.TempC >= p.QuietTempC {
			quietTempSince = s.QuietTempSince
			if quietTempSince.IsZero() {
				quietTempSince = now
			}
		}
		if p.QuietHold > 0 && s.FanSpeedPct >= p.QuietThresholdPct &&
			s.PowerW < p.PowerThresholdW && s.TempC < p.Curve.LowerBound() &&
			writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchManual, Speed: target}, time.Time{}, now, time.Time{}
		}
		if !quietTempSince.IsZero() && now.Sub(quietTempSince) >= p.QuietDelay &&
			writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchManual, Speed: target}, time.Time{}, now, quietTempSince
		}
		if s.PowerW > p.PowerThresholdW && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchManual, Speed: target}, time.Time{}, time.Time{}, time.Time{}
		}
		return Action{Kind: ActionNoop}, time.Time{}, time.Time{}, quietTempSince
	case gpu.ModeManual:
		// The quiet hold lasts QuietHold; load ends it early, and a
		// fan-triggered hold also ends early when the card heats up (a
		// temperature-triggered hold starts at that temperature).
		quietSince := s.QuietSince
		quietTempSince := s.QuietTempSince
		if !quietSince.IsZero() && (s.PowerW >= p.PowerThresholdW ||
			(quietTempSince.IsZero() && s.TempC >= p.Curve.LowerBound())) {
			quietSince = time.Time{}
			quietTempSince = time.Time{}
		}
		if !quietSince.IsZero() && now.Sub(quietSince) >= p.QuietHold && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchAuto}, time.Time{}, time.Time{}, time.Time{}
		}

		idle := s.PowerW < p.PowerThresholdW && s.TempC < p.Curve.LowerBound()
		if !idle {
			// Load present or the card is still hot: no cooldown, follow the curve.
			if math.Abs(target-s.FanSpeedPct) >= p.DeadbandPct && writeAllowed(s.LastWrite, now, p.WritePause) {
				return Action{Kind: ActionUpdateSpeed, Speed: target}, time.Time{}, quietSince, quietTempSince
			}
			return Action{Kind: ActionNoop}, time.Time{}, quietSince, quietTempSince
		}

		// Idle conditions hold: the auto-cooldown.
		idleSince := s.IdleSince
		if idleSince.IsZero() {
			idleSince = now // cooldown starts on this tick
		}
		if now.Sub(idleSince) >= p.AutoDelay && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchAuto}, time.Time{}, time.Time{}, time.Time{}
		}
		// Hold the minimum speed while waiting (deadband + pause apply).
		if math.Abs(curve.MinSpeed-s.FanSpeedPct) >= p.DeadbandPct && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionUpdateSpeed, Speed: curve.MinSpeed}, idleSince, quietSince, quietTempSince
		}
		return Action{Kind: ActionNoop}, idleSince, quietSince, quietTempSince
	default:
		return Action{Kind: ActionNoop}, time.Time{}, time.Time{}, time.Time{}
	}
}
