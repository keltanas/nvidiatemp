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
	PowerThresholdW float64 // W; manual mode entry above this power
	DeadbandPct     float64 // %; minimum |target − current| to write
	WritePause      time.Duration
	AutoDelay       time.Duration // auto-cooldown before returning to the driver
	Curve           curve.Curve
}

// State is the per-GPU input of one tick decision.
type State struct {
	TempC       float64
	PowerW      float64
	FanSpeedPct float64 // current speed as measured from the hardware
	Mode        gpu.Mode
	LastWrite   time.Time // zero means «never written»
	IdleSince   time.Time // auto-cooldown start; zero means «not cooling down»
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

// Tick decides the action for one GPU from its current state and returns
// the auto-cooldown start to track going forward (zero = not cooling down):
//
//   - auto → manual when power exceeds the threshold (speed from the curve);
//   - in manual mode, when power is below the threshold AND temperature is
//     below the lower bound of the curve the GPU enters the auto-cooldown:
//     the fan is held at the minimum speed, and after AutoDelay without load
//     it returns to auto mode; any break of the idle conditions cancels the
//     cooldown and control follows the curve again;
//   - in manual mode outside the cooldown the speed is written only when
//     |target − current| is at least the deadband;
//   - every write (mode or speed) is subject to the pause between writes —
//     this also damps mode flapping (design D4).
func Tick(p Params, s State, now time.Time) (Action, time.Time) {
	target := p.Curve.SpeedAt(s.TempC) // clamped by the curve to [30, 100]

	switch s.Mode {
	case gpu.ModeAuto:
		if s.PowerW > p.PowerThresholdW && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchManual, Speed: target}, time.Time{}
		}
		return Action{Kind: ActionNoop}, time.Time{}
	case gpu.ModeManual:
		idle := s.PowerW < p.PowerThresholdW && s.TempC < p.Curve.LowerBound()
		if !idle {
			// Load present or the card is still hot: no cooldown, follow the curve.
			if math.Abs(target-s.FanSpeedPct) >= p.DeadbandPct && writeAllowed(s.LastWrite, now, p.WritePause) {
				return Action{Kind: ActionUpdateSpeed, Speed: target}, time.Time{}
			}
			return Action{Kind: ActionNoop}, time.Time{}
		}

		// Idle conditions hold: the auto-cooldown.
		idleSince := s.IdleSince
		if idleSince.IsZero() {
			idleSince = now // cooldown starts on this tick
		}
		if now.Sub(idleSince) >= p.AutoDelay && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionSwitchAuto}, time.Time{}
		}
		// Hold the minimum speed while waiting (deadband + pause apply).
		if math.Abs(curve.MinSpeed-s.FanSpeedPct) >= p.DeadbandPct && writeAllowed(s.LastWrite, now, p.WritePause) {
			return Action{Kind: ActionUpdateSpeed, Speed: curve.MinSpeed}, idleSince
		}
		return Action{Kind: ActionNoop}, idleSince
	default:
		return Action{Kind: ActionNoop}, time.Time{}
	}
}
