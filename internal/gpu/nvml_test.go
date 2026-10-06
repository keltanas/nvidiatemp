package gpu

import (
	"errors"
	"strings"
	"testing"
)

// fakeNvmlDevice implements nvmlDevice without any GPU.
type fakeNvmlDevice struct {
	index    int
	name     string
	tempC    float64
	powerW   float64
	speeds   []float64 // one current speed per fan
	policies []Mode    // one current policy per fan
	// fail, when set, is returned by every call.
	fail error
	// writeFail, when set, is returned by the setters only.
	writeFail error

	setPolicies []Mode
	setSpeeds   []float64
}

func (d *fakeNvmlDevice) Index() (int, error)            { return d.index, d.fail }
func (d *fakeNvmlDevice) Name() (string, error)          { return d.name, d.fail }
func (d *fakeNvmlDevice) TemperatureC() (float64, error) { return d.tempC, d.fail }
func (d *fakeNvmlDevice) PowerW() (float64, error)       { return d.powerW, d.fail }
func (d *fakeNvmlDevice) FanCount() (int, error)         { return len(d.speeds), d.fail }

func (d *fakeNvmlDevice) FanSpeedPct(fan int) (float64, error) {
	if d.fail != nil {
		return 0, d.fail
	}
	return d.speeds[fan], nil
}

func (d *fakeNvmlDevice) FanControlMode(fan int) (Mode, error) {
	if d.fail != nil {
		return ModeAuto, d.fail
	}
	return d.policies[fan], nil
}

func (d *fakeNvmlDevice) SetFanControlMode(fan int, mode Mode) error {
	if d.writeFail != nil {
		return d.writeFail
	}
	if d.fail != nil {
		return d.fail
	}
	d.policies[fan] = mode
	d.setPolicies = append(d.setPolicies, mode)
	return nil
}

func (d *fakeNvmlDevice) SetFanSpeedPct(fan int, pct float64) error {
	if d.writeFail != nil {
		return d.writeFail
	}
	if d.fail != nil {
		return d.fail
	}
	d.speeds[fan] = pct
	d.setSpeeds = append(d.setSpeeds, pct)
	return nil
}

// fakeNvmlAPI implements nvmlAPI over fake devices.
type fakeNvmlAPI struct {
	devices  []*fakeNvmlDevice
	initErr  error
	fail     error
	shutdown bool
}

func (a *fakeNvmlAPI) Init() error {
	if a.initErr != nil {
		return a.initErr
	}
	return nil
}

func (a *fakeNvmlAPI) Shutdown() { a.shutdown = true }

func (a *fakeNvmlAPI) DeviceCount() (int, error) {
	if a.fail != nil {
		return 0, a.fail
	}
	return len(a.devices), nil
}

func (a *fakeNvmlAPI) Device(index int) (nvmlDevice, error) {
	if a.fail != nil {
		return nil, a.fail
	}
	if index < 0 || index >= len(a.devices) {
		return nil, errors.New("no such device")
	}
	return a.devices[index], nil
}

func newTestNVML(api nvmlAPI) *NVML { return &NVML{api: api} }

// Two cards, the first with two fans: Read reports the highest fan speed and
// the per-GPU identity from NVML.
func TestNVMLRead(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{
		{index: 0, name: "GeForce RTX 4080", tempC: 62, powerW: 210.5, speeds: []float64{20, 44}, policies: []Mode{ModeAuto, ModeAuto}},
		{index: 1, name: "GeForce RTX 3060", tempC: 45, powerW: 35.25, speeds: []float64{30}, policies: []Mode{ModeAuto}},
	}}
	infos, err := newTestNVML(api).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("infos = %d, want 2", len(infos))
	}
	first := infos[0]
	if first.Index != 0 || first.Name != "GeForce RTX 4080" || first.TempC != 62 || first.PowerW != 210.5 {
		t.Errorf("first = %+v", first)
	}
	if first.FanSpeedPct != 44 {
		t.Errorf("first fan speed = %v, want the highest of the two fans (44)", first.FanSpeedPct)
	}
	if infos[1].FanSpeedPct != 30 || infos[1].PowerW != 35.25 {
		t.Errorf("second = %+v", infos[1])
	}
}

// No visible GPU is the clear ErrNoGPUs, not an empty result.
func TestNVMLReadNoGPUs(t *testing.T) {
	if _, err := newTestNVML(&fakeNvmlAPI{}).Read(); !errors.Is(err, ErrNoGPUs) {
		t.Fatalf("error = %v, want ErrNoGPUs", err)
	}
}

// A fanless GPU (laptop dGPU sleep state) does not fail the poll.
func TestNVMLReadFanlessGPU(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{{index: 0, name: "fanless", tempC: 40, powerW: 5}}}
	infos, err := newTestNVML(api).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if infos[0].FanSpeedPct != 0 {
		t.Errorf("fan speed = %v, want 0", infos[0].FanSpeedPct)
	}
}

// SetManual addresses every fan of the GPU and rounds the speed to whole
// percent, switching the policy to manual first.
func TestNVMLSetManualAllFans(t *testing.T) {
	dev := &fakeNvmlDevice{index: 0, speeds: []float64{0, 0}, policies: []Mode{ModeAuto, ModeAuto}}
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{dev}}

	if err := newTestNVML(api).SetManual(0, 42.6); err != nil {
		t.Fatalf("SetManual: %v", err)
	}
	if len(dev.setPolicies) != 2 || dev.setPolicies[0] != ModeManual || dev.setPolicies[1] != ModeManual {
		t.Errorf("policies written = %v, want manual twice", dev.setPolicies)
	}
	if len(dev.setSpeeds) != 2 || dev.setSpeeds[0] != 43 || dev.setSpeeds[1] != 43 {
		t.Errorf("speeds written = %v, want [43 43]", dev.setSpeeds)
	}
}

// SetManual on a GPU without controllable fans is an error, not a silent
// no-op.
func TestNVMLSetManualNoFans(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{{index: 0}}}
	err := newTestNVML(api).SetManual(0, 40)
	if err == nil || !strings.Contains(err.Error(), "no controllable fans") {
		t.Fatalf("error = %v, want a no-controllable-fans error", err)
	}
}

// SetAuto returns every fan to the driver policy.
func TestNVMLSetAutoAllFans(t *testing.T) {
	dev := &fakeNvmlDevice{index: 0, speeds: []float64{40, 40}, policies: []Mode{ModeManual, ModeManual}}
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{dev}}

	if err := newTestNVML(api).SetAuto(0); err != nil {
		t.Fatalf("SetAuto: %v", err)
	}
	if len(dev.setPolicies) != 2 || dev.setPolicies[0] != ModeAuto || dev.setPolicies[1] != ModeAuto {
		t.Errorf("policies written = %v, want auto twice", dev.setPolicies)
	}
	if dev.policies[0] != ModeAuto || dev.policies[1] != ModeAuto {
		t.Errorf("policies now = %v, want auto", dev.policies)
	}
}

// A GPU counts as manual when any of its fans is in manual mode.
func TestNVMLReadModes(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{
		{index: 0, speeds: []float64{40, 40}, policies: []Mode{ModeAuto, ModeManual}},
		{index: 1, speeds: []float64{30}, policies: []Mode{ModeAuto}},
	}}
	modes, err := newTestNVML(api).ReadModes()
	if err != nil {
		t.Fatalf("ReadModes: %v", err)
	}
	if modes[0] != ModeManual {
		t.Errorf("GPU 0 mode = %v, want manual (one fan manual)", modes[0])
	}
	if modes[1] != ModeAuto {
		t.Errorf("GPU 1 mode = %v, want auto", modes[1])
	}
}

// CheckWritable re-applies the current policy of every fan: a no-op write
// that proves the write path works before the daemon manages anything.
func TestNVMLCheckWritable(t *testing.T) {
	dev := &fakeNvmlDevice{index: 0, speeds: []float64{30, 30}, policies: []Mode{ModeAuto, ModeManual}}
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{dev}}

	if err := newTestNVML(api).CheckWritable(); err != nil {
		t.Fatalf("CheckWritable: %v", err)
	}
	if len(dev.setPolicies) != 2 {
		t.Fatalf("policy writes = %v, want each fan's current policy re-applied", dev.setPolicies)
	}
	if dev.policies[0] != ModeAuto || dev.policies[1] != ModeManual {
		t.Errorf("policies changed by the probe: %v", dev.policies)
	}
}

// A read-only or unsupported card is caught before the control loop starts.
func TestNVMLCheckWritableRejectsReadOnlyCard(t *testing.T) {
	dev := &fakeNvmlDevice{index: 0, speeds: []float64{30}, policies: []Mode{ModeAuto}, writeFail: errors.New("NVML_ERROR_NOT_SUPPORTED")}
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{dev}}

	err := newTestNVML(api).CheckWritable()
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error = %v, want a not-writable error", err)
	}
}

// A machine whose GPUs expose no fans cannot be managed through NVML.
func TestNVMLCheckWritableNoFans(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{{index: 0}}}
	err := newTestNVML(api).CheckWritable()
	if err == nil || !strings.Contains(err.Error(), "no controllable fans") {
		t.Fatalf("error = %v, want a no-controllable-fans error", err)
	}
}

func TestNVMLCheckWritableNoGPUs(t *testing.T) {
	if err := newTestNVML(&fakeNvmlAPI{}).CheckWritable(); !errors.Is(err, ErrNoGPUs) {
		t.Fatalf("error = %v, want ErrNoGPUs", err)
	}
}

// Close releases the library.
func TestNVMLClose(t *testing.T) {
	api := &fakeNvmlAPI{}
	newTestNVML(api).Close()
	if !api.shutdown {
		t.Error("Close did not shut the NVML library down")
	}
}

// Pure conversions used by the real adapter.
func TestNVMLConversions(t *testing.T) {
	if got := roundSpeedPct(42.6); got != 43 {
		t.Errorf("roundSpeedPct(42.6) = %d, want 43", got)
	}
	if got := roundSpeedPct(42.4); got != 42 {
		t.Errorf("roundSpeedPct(42.4) = %d, want 42", got)
	}
	if got := milliWattsToWatts(210500); got != 210.5 {
		t.Errorf("milliWattsToWatts(210500) = %v, want 210.5", got)
	}
}
