package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"nvidiatemp/internal/curve"
	"nvidiatemp/internal/gpu"
)

// scriptedReader returns results in order, repeating the last one.
type readResult struct {
	infos []gpu.Info
	err   error
}

type scriptedReader struct {
	results []readResult
	i       int
	calls   int
}

func (s *scriptedReader) Read() ([]gpu.Info, error) {
	s.calls++
	r := s.results[min(s.i, len(s.results)-1)]
	s.i++
	return r.infos, r.err
}

// fakeSetter records the last write per GPU.
type fakeSetter struct {
	manual map[int]float64
	auto   []int
	err    error
}

func (f *fakeSetter) SetManual(index int, speed float64) error {
	if f.err != nil {
		return f.err
	}
	if f.manual == nil {
		f.manual = map[int]float64{}
	}
	f.manual[index] = speed
	return nil
}

func (f *fakeSetter) SetAuto(index int) error {
	f.auto = append(f.auto, index)
	return f.err
}

type fakeModes struct {
	modes map[int]gpu.Mode
	err   error
}

func (f *fakeModes) ReadModes() (map[int]gpu.Mode, error) { return f.modes, f.err }

// One tick: a power spike takes the GPU into manual mode at the curve speed.
func TestDaemonProcessEntry(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	states := map[int]*gpuState{0: {mode: gpu.ModeAuto}}
	now := time.Now()

	d.process([]gpu.Info{{Index: 0, TempC: 62, PowerW: 80, FanSpeedPct: 30}}, states, now)

	if states[0].mode != gpu.ModeManual {
		t.Errorf("mode = %v, want manual", states[0].mode)
	}
	if setter.manual[0] != 42 {
		t.Errorf("speed written = %v, want 42 (curve at 62°C)", setter.manual[0])
	}
	if !states[0].lastWrite.Equal(now) {
		t.Error("lastWrite must be updated on a write")
	}
}

// Two-phase exit: an idle cold GPU in manual mode first enters the
// auto-cooldown (fan dropped to minimum, still manual) and only switches to
// auto after AutoDelay has elapsed without load.
func TestDaemonProcessExit(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	states := map[int]*gpuState{0: {mode: gpu.ModeManual}}
	now := time.Now()

	// First idle tick: cooldown starts, fan dropped to minimum, still manual.
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 60}}, states, now)
	if states[0].mode != gpu.ModeManual {
		t.Errorf("mode after first idle tick = %v, want manual (cooldown)", states[0].mode)
	}
	if setter.manual[0] != curve.MinSpeed {
		t.Errorf("speed written = %v, want minimum %v", setter.manual[0], curve.MinSpeed)
	}
	if states[0].idleSince.IsZero() {
		t.Error("cooldown must be started on the first idle tick")
	}

	// Before AutoDelay elapses: still manual.
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}, states, now.Add(4*time.Minute))
	if states[0].mode != gpu.ModeManual {
		t.Errorf("mode before the delay = %v, want manual", states[0].mode)
	}

	// After AutoDelay elapses (and the write pause): auto.
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}, states, now.Add(5*time.Minute))
	if states[0].mode != gpu.ModeAuto {
		t.Errorf("mode after the delay = %v, want auto", states[0].mode)
	}
	if len(setter.auto) != 1 || setter.auto[0] != 0 {
		t.Errorf("auto calls = %v, want [0]", setter.auto)
	}
}

// The driver spins the fan up on an idle cold card: the daemon takes it
// over, holds it quiet for QuietHold and then returns it to the driver.
func TestDaemonProcessQuietHold(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	states := map[int]*gpuState{0: {mode: gpu.ModeAuto}}
	now := time.Now()

	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 100}}, states, now)
	if states[0].mode != gpu.ModeManual || setter.manual[0] != 30 {
		t.Fatalf("mode = %v, speed = %v; want manual at 30", states[0].mode, setter.manual[0])
	}
	if !states[0].quietSince.Equal(now) {
		t.Fatalf("quietSince = %v, want the current tick time", states[0].quietSince)
	}

	// Hold in progress: still manual.
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}, states, now.Add(2*time.Minute))
	if states[0].mode != gpu.ModeManual {
		t.Errorf("mode during the hold = %v, want manual", states[0].mode)
	}

	// Hold elapsed: back to the driver.
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}, states, now.Add(3*time.Minute))
	if states[0].mode != gpu.ModeAuto {
		t.Errorf("mode after the hold = %v, want auto", states[0].mode)
	}
	if !states[0].quietSince.IsZero() {
		t.Errorf("quietSince must be reset, got %v", states[0].quietSince)
	}
	if len(setter.auto) != 1 || setter.auto[0] != 0 {
		t.Errorf("auto calls = %v, want [0]", setter.auto)
	}
}

// An idle card that has stayed at quiet_temp for quiet_delay is taken over
// even though the driver keeps the fan low, held for QuietHold and returned.
func TestDaemonProcessQuietTempHold(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	now := time.Now()
	states := map[int]*gpuState{0: {mode: gpu.ModeAuto, quietTempSince: now.Add(-time.Minute)}}

	d.process([]gpu.Info{{Index: 0, TempC: 50, PowerW: 10, FanSpeedPct: 20}}, states, now)
	if states[0].mode != gpu.ModeManual || setter.manual[0] != 30 {
		t.Fatalf("mode = %v, speed = %v; want manual at 30 (curve at 50°C)", states[0].mode, setter.manual[0])
	}
	if !states[0].quietSince.Equal(now) {
		t.Fatalf("quietSince = %v, want the current tick time", states[0].quietSince)
	}

	// Hold elapsed: back to the driver, both markers reset.
	d.process([]gpu.Info{{Index: 0, TempC: 50, PowerW: 10, FanSpeedPct: 30}}, states, now.Add(3*time.Minute))
	if states[0].mode != gpu.ModeAuto {
		t.Errorf("mode after the hold = %v, want auto", states[0].mode)
	}
	if !states[0].quietSince.IsZero() || !states[0].quietTempSince.IsZero() {
		t.Errorf("quietSince = %v, quietTempSince = %v; want both reset",
			states[0].quietSince, states[0].quietTempSince)
	}
}

func TestDaemonProcessCooldownCancelled(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	states := map[int]*gpuState{0: {mode: gpu.ModeManual}}
	now := time.Now()

	// Idle → cooldown starts (fan already at the minimum).
	d.process([]gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}, states, now)
	if states[0].idleSince.IsZero() {
		t.Fatal("cooldown not started")
	}
	// Load returns before the delay → cooldown cancelled, curve resumes.
	d.process([]gpu.Info{{Index: 0, TempC: 62, PowerW: 80, FanSpeedPct: 30}}, states, now.Add(1*time.Minute))
	if !states[0].idleSince.IsZero() {
		t.Errorf("cooldown must be cancelled, got %v", states[0].idleSince)
	}
	if setter.manual[0] != 42 {
		t.Errorf("speed written = %v, want 42 (curve at 62°C)", setter.manual[0])
	}
}

// A GPU that appears mid-run is managed from scratch (assumed auto).
func TestDaemonProcessNewGPU(t *testing.T) {
	p := defaultParams()
	setter := &fakeSetter{}
	d := NewDaemon(p, &scriptedReader{}, setter)
	states := map[int]*gpuState{}

	d.process([]gpu.Info{{Index: 3, TempC: 62, PowerW: 80, FanSpeedPct: 30}}, states, time.Now())

	if _, ok := states[3]; !ok {
		t.Fatal("new GPU not tracked")
	}
	if states[3].mode != gpu.ModeManual || setter.manual[3] != 42 {
		t.Errorf("new GPU state = %+v, speed = %v; want manual at 42", states[3], setter.manual[3])
	}
}

// Spec scenario «Устойчивая ошибка опроса»: 5 consecutive failed polls →
// auto restored on all GPUs + ErrSustainedPollFailure (exit non-zero).
func TestDaemonSustainedFailure(t *testing.T) {
	p := defaultParams()
	reader := &scriptedReader{results: []readResult{
		{infos: []gpu.Info{{Index: 0, TempC: 45, PowerW: 10, FanSpeedPct: 30}}}, // initial OK
		{err: errors.New("driver gone")},                                        // then always fails
	}}
	setter := &fakeSetter{}
	d := NewDaemon(p, reader, setter)
	d.PollInterval = time.Millisecond

	err := d.Run(context.Background())
	if !errors.Is(err, ErrSustainedPollFailure) {
		t.Fatalf("error = %v, want ErrSustainedPollFailure", err)
	}
	if len(setter.auto) != 1 || setter.auto[0] != 0 {
		t.Errorf("auto restore calls = %v, want [0]", setter.auto)
	}
}

// Clean shutdown (ctx cancel): auto restored on all managed GPUs, nil error.
func TestDaemonShutdownRestoresAuto(t *testing.T) {
	p := defaultParams()
	reader := &scriptedReader{results: []readResult{
		{infos: []gpu.Info{{Index: 0, TempC: 62, PowerW: 80, FanSpeedPct: 30}}}, // hot under load → manual
	}}
	setter := &fakeSetter{}
	d := NewDaemon(p, reader, setter)
	d.PollInterval = time.Hour // no further ticks after the first poll

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	time.Sleep(50 * time.Millisecond) // let the first poll + tick happen
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown error = %v, want nil", err)
	}
	if setter.manual[0] != 42 {
		t.Errorf("expected a manual switch at 42 before shutdown, got %v", setter.manual[0])
	}
	if len(setter.auto) != 1 || setter.auto[0] != 0 {
		t.Errorf("auto restore calls = %v, want [0]", setter.auto)
	}
}

// No visible GPUs: immediate clear error, no hang.
func TestDaemonNoGPUs(t *testing.T) {
	p := defaultParams()
	reader := &scriptedReader{results: []readResult{{err: gpu.ErrNoGPUs}}}
	d := NewDaemon(p, reader, &fakeSetter{})

	err := d.Run(context.Background())
	if !errors.Is(err, gpu.ErrNoGPUs) {
		t.Fatalf("error = %v, want ErrNoGPUs", err)
	}
}

// Initial modes are read from the hardware once at startup (design D3): a
// card left in manual mode is picked up as manual.
func TestDaemonSeedsInitialModes(t *testing.T) {
	p := defaultParams()
	reader := &scriptedReader{results: []readResult{
		// hot, already at the curve speed (42 at 62°C) → stays manual, no write
		{infos: []gpu.Info{{Index: 0, TempC: 62, PowerW: 80, FanSpeedPct: 42}}},
	}}
	setter := &fakeSetter{}
	d := NewDaemon(p, reader, setter)
	d.PollInterval = time.Hour
	d.Modes = &fakeModes{modes: map[int]gpu.Mode{0: gpu.ModeManual}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	// Seeded as manual and already at the target speed: no manual write
	// during the tick (the shutdown restore-all is expected separately).
	if len(setter.manual) != 0 {
		t.Errorf("unexpected manual writes: %v", setter.manual)
	}
}
