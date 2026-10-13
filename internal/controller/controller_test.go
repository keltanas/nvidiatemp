package controller

import (
	"testing"
	"time"

	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

// defaultParams mirrors the built-in defaults: 50W / 5% / 5s / 5m, default curve.
func defaultParams() Params {
	return Params{
		PowerThresholdW:   50,
		DeadbandPct:       5,
		WritePause:        5 * time.Second,
		AutoDelay:         5 * time.Minute,
		QuietThresholdPct: 30,
		QuietHold:         3 * time.Minute,
		Curve: curve.Curve{
			{Temp: 50, Speed: 30},
			{Temp: 60, Speed: 40},
			{Temp: 65, Speed: 45},
			{Temp: 70, Speed: 55},
			{Temp: 75, Speed: 60},
			{Temp: 80, Speed: 70},
		},
	}
}

var now = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func TestTickEntryOnPowerAboveThreshold(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 62, PowerW: 51, FanSpeedPct: 30, Mode: gpu.ModeAuto}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionSwitchManual {
		t.Fatalf("action = %v, want switch-manual", a.Kind)
	}
	if a.Speed != 42 { // 62°C between (60→40) and (65→45)
		t.Errorf("speed = %v, want 42 (from the curve)", a.Speed)
	}
	if !idleSince.IsZero() {
		t.Errorf("auto mode must not start a cooldown, got %v", idleSince)
	}
}

// Entry requires power strictly above the threshold.
func TestTickNoEntryAtExactThreshold(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 62, PowerW: 50, FanSpeedPct: 30, Mode: gpu.ModeAuto}
	if a, _, _ := Tick(p, s, now); a.Kind != ActionNoop {
		t.Errorf("action at power == threshold = %v, want noop", a.Kind)
	}
}

// Spec scenario «Мощность упала, но карта ещё горячая»: power below the
// threshold but temperature at/above the lower bound → stays manual, no
// cooldown (current speed equals the target, so no write either).
func TestTickStaysManualWhileHot(t *testing.T) {
	p := defaultParams()
	for temp, current := range map[float64]float64{50: 30, 75: 60} { // == and > lower bound
		s := State{TempC: temp, PowerW: 12, FanSpeedPct: current, Mode: gpu.ModeManual}
		if a, idleSince, _ := Tick(p, s, now); a.Kind != ActionNoop || !idleSince.IsZero() {
			t.Errorf("temp %v°C: action = %v, idleSince = %v; want noop without cooldown", temp, a.Kind, idleSince)
		}
	}
}

// Still hot: the GPU stays in manual mode — no cooldown, and a speed write
// happens per the curve.
func TestTickStaysManualButUpdatesSpeedWhileHot(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 75, PowerW: 12, FanSpeedPct: 40, Mode: gpu.ModeManual} // target 60
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind == ActionSwitchAuto {
		t.Fatalf("switched to auto while the card is still hot")
	}
	if !idleSince.IsZero() {
		t.Errorf("no cooldown expected while hot, got %v", idleSince)
	}
	if a.Kind != ActionUpdateSpeed || a.Speed != 60 {
		t.Errorf("action = %+v, want update-speed 60", a)
	}
}

// Exit requires BOTH conditions: cold card but power above the threshold
// stays manual with no cooldown.
func TestTickStaysManualWhenPowerHighEvenIfCold(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 45, PowerW: 60, FanSpeedPct: 30, Mode: gpu.ModeManual}
	if a, idleSince, _ := Tick(p, s, now); a.Kind != ActionNoop || !idleSince.IsZero() {
		t.Errorf("action = %v, idleSince = %v; want noop without cooldown (power above threshold)", a.Kind, idleSince)
	}
}

// Spec scenario «Отклонение меньше мёртвой зоны»: |target − current| < 5%
// → no write.
func TestTickDeadbandBelow(t *testing.T) {
	p := defaultParams()
	// temp 62 → target 42; current 40 → deviation 2 < 5.
	s := State{TempC: 62, PowerW: 60, FanSpeedPct: 40, Mode: gpu.ModeManual}
	if a, _, _ := Tick(p, s, now); a.Kind != ActionNoop {
		t.Errorf("action = %v, want noop (deviation below deadband)", a.Kind)
	}
}

// Deviation exactly at the deadband triggers a write («не меньше чем»).
func TestTickDeadbandBoundary(t *testing.T) {
	p := defaultParams()
	// temp 65 → target 45; current 40 → deviation 5 == deadband.
	s := State{TempC: 65, PowerW: 60, FanSpeedPct: 40, Mode: gpu.ModeManual}
	a, _, _ := Tick(p, s, now)
	if a.Kind != ActionUpdateSpeed || a.Speed != 45 {
		t.Errorf("action = %+v, want update-speed 45", a)
	}
}

// Deviation above the deadband triggers a speed update.
func TestTickSpeedUpdate(t *testing.T) {
	p := defaultParams()
	// temp 70 → target 55; current 40 → deviation 15 ≥ 5.
	s := State{TempC: 70, PowerW: 60, FanSpeedPct: 40, Mode: gpu.ModeManual}
	a, _, _ := Tick(p, s, now)
	if a.Kind != ActionUpdateSpeed || a.Speed != 55 {
		t.Errorf("action = %+v, want update-speed 55", a)
	}
}

// Spec scenario «Пауза между записями не истекла»: deviation ≥ deadband but
// the last write is younger than the pause → deferred; after the pause the
// write happens if the deviation persists.
func TestTickWritePauseDefersSpeedUpdate(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 70, PowerW: 60, FanSpeedPct: 40, Mode: gpu.ModeManual} // target 55

	// Last write 4s ago → the 5s pause has not elapsed.
	s.LastWrite = now.Add(-4 * time.Second)
	if a, _, _ := Tick(p, s, now); a.Kind != ActionNoop {
		t.Errorf("at +4s action = %v, want noop (pause not elapsed)", a.Kind)
	}
	// Last write 5s ago → pause elapsed, deviation persists → write.
	s.LastWrite = now.Add(-5 * time.Second)
	if a, _, _ := Tick(p, s, now); a.Kind != ActionUpdateSpeed {
		t.Errorf("at +5s action = %v, want update-speed", a.Kind)
	}
}

// The pause also damps mode flapping: an entry into manual right after any
// write is deferred.
func TestTickWritePauseDefersModeSwitch(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 62, PowerW: 80, FanSpeedPct: 30, Mode: gpu.ModeAuto, LastWrite: now.Add(-1 * time.Second)}
	if a, _, _ := Tick(p, s, now); a.Kind != ActionNoop {
		t.Errorf("action = %v, want noop (last write 1s ago)", a.Kind)
	}
	s.LastWrite = now.Add(-5 * time.Second)
	if a, _, _ := Tick(p, s, now); a.Kind != ActionSwitchManual {
		t.Errorf("action = %v, want switch-manual after the pause", a.Kind)
	}
}

// Spec scenario «Клампинг выше последней точки»: temp above the curve →
// the last point's speed; always within [30, 100].
func TestTickTargetClampedAboveCurve(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 95, PowerW: 60, FanSpeedPct: 40, Mode: gpu.ModeManual}
	a, _, _ := Tick(p, s, now)
	if a.Kind != ActionUpdateSpeed || a.Speed != 70 {
		t.Errorf("action = %+v, want update-speed 70 (clamped)", a)
	}
	if a.Speed < 30 || a.Speed > 100 {
		t.Errorf("speed %v outside [30, 100]", a.Speed)
	}
}

// First idle tick: the auto-cooldown starts; no switch to auto yet. The fan
// is already at the minimum → just tracking begins.
func TestTickIdleStartsCooldown(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 49, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionNoop {
		t.Errorf("action = %+v, want noop (fan already at minimum)", a)
	}
	if !idleSince.Equal(now) {
		t.Errorf("idleSince = %v, want the current tick time", idleSince)
	}
}

// First idle tick with the fan above the minimum: it is dropped to the
// minimum speed while the cooldown starts.
func TestTickIdleStartsCooldownAndDropsToMinimum(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 49, PowerW: 12, FanSpeedPct: 55, Mode: gpu.ModeManual}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionUpdateSpeed || a.Speed != curve.MinSpeed {
		t.Errorf("action = %+v, want update-speed %v (minimum)", a, curve.MinSpeed)
	}
	if !idleSince.Equal(now) {
		t.Errorf("idleSince = %v, want the current tick time", idleSince)
	}
}

// Cooldown in progress but AutoDelay not elapsed: no switch, state kept.
func TestTickCooldownNotYetElapsed(t *testing.T) {
	p := defaultParams()
	started := now.Add(-4 * time.Minute)
	s := State{TempC: 49, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual, IdleSince: started}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionNoop {
		t.Errorf("action = %+v, want noop (cooldown in progress)", a)
	}
	if !idleSince.Equal(started) {
		t.Errorf("idleSince = %v, want the original start %v", idleSince, started)
	}
}

// Spec scenario «Оба условия выполнены» + AutoDelay elapsed: switch to auto.
func TestTickCooldownElapsedSwitchesToAuto(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 49, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual, IdleSince: now.Add(-5 * time.Minute)}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionSwitchAuto {
		t.Errorf("action = %+v, want switch-auto after the delay", a)
	}
	if !idleSince.IsZero() {
		t.Errorf("idleSince must reset on the switch, got %v", idleSince)
	}
}

// The final switch to auto is subject to the write pause: if a write just
// happened, the switch is deferred and the cooldown keeps running.
func TestTickCooldownSwitchRespectsWritePause(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 49, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual,
		IdleSince: now.Add(-6 * time.Minute), LastWrite: now.Add(-1 * time.Second)}
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionNoop {
		t.Errorf("action = %+v, want noop (write pause not elapsed)", a)
	}
	if !idleSince.Equal(now.Add(-6 * time.Minute)) {
		t.Errorf("cooldown must keep running, idleSince = %v", idleSince)
	}
}

// Load returns during the cooldown (power above the threshold): the cooldown
// is cancelled and control follows the curve again.
func TestTickCooldownCancelledByPower(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 62, PowerW: 80, FanSpeedPct: 30, Mode: gpu.ModeManual, IdleSince: now.Add(-2 * time.Minute)}
	a, idleSince, _ := Tick(p, s, now)
	if !idleSince.IsZero() {
		t.Errorf("cooldown must be cancelled, got idleSince = %v", idleSince)
	}
	// target at 62°C is 42; deviation from 30 ≥ deadband → write.
	if a.Kind != ActionUpdateSpeed || a.Speed != 42 {
		t.Errorf("action = %+v, want update-speed 42 (curve control resumes)", a)
	}
}

// The card warms back above the lower bound during the cooldown: cancelled,
// curve control resumes.
func TestTickCooldownCancelledByTemp(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 55, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual, IdleSince: now.Add(-2 * time.Minute)}
	a, idleSince, _ := Tick(p, s, now)
	if !idleSince.IsZero() {
		t.Errorf("cooldown must be cancelled, got idleSince = %v", idleSince)
	}
	// target at 55°C is 35; deviation from 30 == deadband → write.
	if a.Kind != ActionUpdateSpeed || a.Speed != 35 {
		t.Errorf("action = %+v, want update-speed 35 (curve control resumes)", a)
	}
}

// The driver spins the fan up on its own while the card is idle and cold:
// nvidiatemp takes the card over at the curve speed (30% at idle with the
// default curve) and starts a quiet hold.
func TestTickQuietHoldCatchesDriverFanSpinUp(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 45, PowerW: 12, FanSpeedPct: 100, Mode: gpu.ModeAuto}
	a, _, quietSince := Tick(p, s, now)
	if a.Kind != ActionSwitchManual || a.Speed != 30 {
		t.Errorf("action = %+v, want switch-manual 30 (curve)", a)
	}
	if !quietSince.Equal(now) {
		t.Errorf("quietSince = %v, want the current tick time", quietSince)
	}
}

// No quiet hold below the threshold: a card sitting at the minimum speed in
// auto mode is left to the driver.
func TestTickNoQuietHoldBelowThreshold(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 45, PowerW: 12, FanSpeedPct: 29, Mode: gpu.ModeAuto}
	if a, _, quietSince := Tick(p, s, now); a.Kind != ActionNoop || !quietSince.IsZero() {
		t.Errorf("action = %v, quietSince = %v; want noop", a.Kind, quietSince)
	}
}

// The card is genuinely hot: the driver is allowed to cool it, no quiet hold.
func TestTickNoQuietHoldWhenHot(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 55, PowerW: 12, FanSpeedPct: 100, Mode: gpu.ModeAuto}
	if a, _, quietSince := Tick(p, s, now); a.Kind != ActionNoop || !quietSince.IsZero() {
		t.Errorf("action = %v, quietSince = %v; want noop (card hot)", a.Kind, quietSince)
	}
}

// Load above the threshold means the fan is doing its job, not running away.
func TestTickNoQuietHoldUnderLoad(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 45, PowerW: 80, FanSpeedPct: 100, Mode: gpu.ModeAuto}
	a, _, quietSince := Tick(p, s, now)
	if a.Kind != ActionSwitchManual || quietSince.IsZero() == false {
		t.Errorf("action = %+v, quietSince = %v; want plain curve entry without a quiet hold", a, quietSince)
	}
}

// quiet_hold: 0 disables the whole feature: the driver keeps the fan.
func TestTickQuietHoldDisabled(t *testing.T) {
	p := defaultParams()
	p.QuietHold = 0
	s := State{TempC: 45, PowerW: 12, FanSpeedPct: 100, Mode: gpu.ModeAuto}
	if a, _, quietSince := Tick(p, s, now); a.Kind != ActionNoop || !quietSince.IsZero() {
		t.Errorf("action = %v, quietSince = %v; want noop (feature disabled)", a.Kind, quietSince)
	}
}

// The hold is over: the card goes back to driver control, the mark resets.
func TestTickQuietHoldElapsedSwitchesToAuto(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 45, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual,
		QuietSince: now.Add(-3 * time.Minute), LastWrite: now.Add(-time.Minute)}
	a, _, quietSince := Tick(p, s, now)
	if a.Kind != ActionSwitchAuto {
		t.Errorf("action = %+v, want switch-auto after the hold", a)
	}
	if !quietSince.IsZero() {
		t.Errorf("quietSince must reset on the switch, got %v", quietSince)
	}
}

// Hold in progress: no switch, the card follows the ordinary curve rules.
func TestTickQuietHoldNotYetElapsed(t *testing.T) {
	p := defaultParams()
	started := now.Add(-2 * time.Minute)
	s := State{TempC: 45, PowerW: 12, FanSpeedPct: 30, Mode: gpu.ModeManual,
		QuietSince: started, LastWrite: now.Add(-time.Minute)}
	a, _, quietSince := Tick(p, s, now)
	if a.Kind == ActionSwitchAuto {
		t.Error("switched to auto while the hold is still running")
	}
	if !quietSince.Equal(started) {
		t.Errorf("quietSince = %v, want the original start %v", quietSince, started)
	}
}

// Load returns during the hold: it ends early, control follows the curve.
func TestTickQuietHoldCancelledByLoad(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 62, PowerW: 80, FanSpeedPct: 30, Mode: gpu.ModeManual,
		QuietSince: now.Add(-2 * time.Minute)}
	a, _, quietSince := Tick(p, s, now)
	if !quietSince.IsZero() {
		t.Errorf("the hold must be cancelled by load, got %v", quietSince)
	}
	if a.Kind != ActionUpdateSpeed || a.Speed != 42 {
		t.Errorf("action = %+v, want update-speed 42 (curve control resumes)", a)
	}
}

// A card left in manual mode at startup: an idle cold card enters the
// cooldown (drops to minimum), not straight to auto.
func TestTickTakesOverCardLeftInManual(t *testing.T) {
	p := defaultParams()
	s := State{TempC: 40, PowerW: 15, FanSpeedPct: 60, Mode: gpu.ModeManual} // LastWrite zero
	a, idleSince, _ := Tick(p, s, now)
	if a.Kind != ActionUpdateSpeed || a.Speed != curve.MinSpeed {
		t.Errorf("action = %+v, want update-speed %v (cooldown start)", a, curve.MinSpeed)
	}
	if !idleSince.Equal(now) {
		t.Errorf("idleSince = %v, want the current tick time", idleSince)
	}
}
