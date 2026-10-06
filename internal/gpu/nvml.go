package gpu

import (
	"errors"
	"fmt"
	"math"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// nvmlDevice is the per-GPU subset of the NVML API the backend uses. It is an
// interface so tests can substitute a fake and run without a GPU.
type nvmlDevice interface {
	Index() (int, error)
	Name() (string, error)
	TemperatureC() (float64, error)
	PowerW() (float64, error)
	FanCount() (int, error)
	FanSpeedPct(fan int) (float64, error)
	FanControlMode(fan int) (Mode, error)
	SetFanControlMode(fan int, mode Mode) error
	SetFanSpeedPct(fan int, pct float64) error
}

// nvmlAPI is the library-level subset of the NVML API the backend uses.
type nvmlAPI interface {
	Init() error
	Shutdown()
	DeviceCount() (int, error)
	Device(index int) (nvmlDevice, error)
}

// NVML is the fan-control backend that talks to the driver through the NVML
// library instead of nvidia-settings: it needs no X display and no sudo, and
// addresses every fan of a GPU explicitly. One value implements Reader,
// FanSetter and ModeReader.
type NVML struct {
	api nvmlAPI
}

// NewNVML initializes NVML and returns the backend. It fails when the NVIDIA
// driver or libnvidia-ml is not available.
func NewNVML() (*NVML, error) {
	n := &NVML{api: realNVML{}}
	if err := n.api.Init(); err != nil {
		return nil, err
	}
	return n, nil
}

// Close releases the NVML library.
func (n *NVML) Close() {
	if n.api != nil {
		n.api.Shutdown()
	}
}

// CheckWritable verifies that fan control can actually be written: for every
// visible GPU the current control policy of every fan is read and re-applied
// (a no-op write). It catches cards whose NVML fan control is read-only or
// unsupported before the daemon starts managing anything, and reports
// ErrNoGPUs when no NVIDIA GPU is visible.
func (n *NVML) CheckWritable() error {
	count, err := n.api.DeviceCount()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNoGPUs
	}
	writable := 0
	for i := 0; i < count; i++ {
		dev, err := n.api.Device(i)
		if err != nil {
			return err
		}
		idx, err := dev.Index()
		if err != nil {
			return err
		}
		fans, err := dev.FanCount()
		if err != nil {
			return err
		}
		for fan := 0; fan < fans; fan++ {
			mode, err := dev.FanControlMode(fan)
			if err != nil {
				return err
			}
			if err := dev.SetFanControlMode(fan, mode); err != nil {
				return fmt.Errorf("nvml: GPU %d fan %d is not writable: %w", idx, fan, err)
			}
			writable++
		}
	}
	if writable == 0 {
		return errors.New("nvml: no controllable fans found")
	}
	return nil
}

// Read polls every visible GPU once.
func (n *NVML) Read() ([]Info, error) {
	count, err := n.api.DeviceCount()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNoGPUs
	}
	infos := make([]Info, 0, count)
	for i := 0; i < count; i++ {
		dev, err := n.api.Device(i)
		if err != nil {
			return nil, err
		}
		info, err := n.info(dev)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// info builds one GPU snapshot. A GPU without controllable fans (laptop
// dGPU sleep states, fanless cards) is reported with a zero fan speed rather
// than failing the whole poll; SetManual on it returns an error.
func (n *NVML) info(dev nvmlDevice) (Info, error) {
	idx, err := dev.Index()
	if err != nil {
		return Info{}, err
	}
	name, err := dev.Name()
	if err != nil {
		return Info{}, err
	}
	temp, err := dev.TemperatureC()
	if err != nil {
		return Info{}, err
	}
	power, err := dev.PowerW()
	if err != nil {
		return Info{}, err
	}
	fanPct, err := maxFanSpeedPct(dev)
	if err != nil {
		return Info{}, err
	}
	return Info{Index: idx, Name: name, TempC: temp, PowerW: power, FanSpeedPct: fanPct}, nil
}

// maxFanSpeedPct returns the highest current fan speed of a GPU, or 0 when it
// has no fans.
func maxFanSpeedPct(dev nvmlDevice) (float64, error) {
	fans, err := dev.FanCount()
	if err != nil {
		return 0, err
	}
	max := 0.0
	for fan := 0; fan < fans; fan++ {
		pct, err := dev.FanSpeedPct(fan)
		if err != nil {
			return 0, err
		}
		if pct > max {
			max = pct
		}
	}
	return max, nil
}

// ReadModes reads the fan control policy of every visible GPU once at startup
// (design D3). A GPU counts as manual when any of its fans is in manual mode.
func (n *NVML) ReadModes() (map[int]Mode, error) {
	count, err := n.api.DeviceCount()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNoGPUs
	}
	modes := make(map[int]Mode, count)
	for i := 0; i < count; i++ {
		dev, err := n.api.Device(i)
		if err != nil {
			return nil, err
		}
		idx, err := dev.Index()
		if err != nil {
			return nil, err
		}
		mode, err := deviceMode(dev)
		if err != nil {
			return nil, err
		}
		modes[idx] = mode
	}
	return modes, nil
}

// deviceMode reports a GPU as manual when any of its fans is manually
// controlled.
func deviceMode(dev nvmlDevice) (Mode, error) {
	fans, err := dev.FanCount()
	if err != nil {
		return ModeAuto, err
	}
	for fan := 0; fan < fans; fan++ {
		mode, err := dev.FanControlMode(fan)
		if err != nil {
			return ModeAuto, err
		}
		if mode == ModeManual {
			return ModeManual, nil
		}
	}
	return ModeAuto, nil
}

// SetManual switches every fan of the GPU to manual control and sets its
// speed in one sequence: policy first, then the target speed.
func (n *NVML) SetManual(index int, speedPct float64) error {
	dev, err := n.api.Device(index)
	if err != nil {
		return err
	}
	fans, err := dev.FanCount()
	if err != nil {
		return err
	}
	if fans == 0 {
		return fmt.Errorf("nvml: GPU %d has no controllable fans", index)
	}
	speed := float64(roundSpeedPct(speedPct))
	for fan := 0; fan < fans; fan++ {
		if err := dev.SetFanControlMode(fan, ModeManual); err != nil {
			return fmt.Errorf("nvml: GPU %d fan %d: switch to manual: %w", index, fan, err)
		}
		if err := dev.SetFanSpeedPct(fan, speed); err != nil {
			return fmt.Errorf("nvml: GPU %d fan %d: set speed %.0f%%: %w", index, fan, speed, err)
		}
	}
	return nil
}

// SetAuto returns every fan of the GPU to the driver's temperature-based
// policy.
func (n *NVML) SetAuto(index int) error {
	dev, err := n.api.Device(index)
	if err != nil {
		return err
	}
	fans, err := dev.FanCount()
	if err != nil {
		return err
	}
	for fan := 0; fan < fans; fan++ {
		if err := dev.SetFanControlMode(fan, ModeAuto); err != nil {
			return fmt.Errorf("nvml: GPU %d fan %d: return to auto: %w", index, fan, err)
		}
	}
	return nil
}

// realNVML adapts github.com/NVIDIA/go-nvml to the nvmlAPI seam.
type realNVML struct{}

func (realNVML) Init() error {
	if r := nvml.Init(); r != nvml.SUCCESS {
		return fmt.Errorf("nvml: init: %w", r)
	}
	return nil
}

// Shutdown releases the library. nvmlShutdown only fails when NVML was not
// initialized, which cannot happen here, and there is nothing to recover
// during process teardown.
func (realNVML) Shutdown() { _ = nvml.Shutdown() }

func (realNVML) DeviceCount() (int, error) {
	count, r := nvml.DeviceGetCount()
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: device count: %w", r)
	}
	return count, nil
}

func (realNVML) Device(index int) (nvmlDevice, error) {
	d, r := nvml.DeviceGetHandleByIndex(index)
	if r != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml: device %d: %w", index, r)
	}
	return realDevice{d: d}, nil
}

// realDevice adapts one nvml.Device to the nvmlDevice seam.
type realDevice struct {
	d nvml.Device
}

func (x realDevice) Index() (int, error) {
	v, r := x.d.GetIndex()
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: get index: %w", r)
	}
	return v, nil
}

func (x realDevice) Name() (string, error) {
	v, r := x.d.GetName()
	if r != nvml.SUCCESS {
		return "", fmt.Errorf("nvml: get name: %w", r)
	}
	return v, nil
}

func (x realDevice) TemperatureC() (float64, error) {
	v, r := x.d.GetTemperature(nvml.TEMPERATURE_GPU)
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: get temperature: %w", r)
	}
	return float64(v), nil
}

// PowerW converts NVML's milliwatts to watts.
func (x realDevice) PowerW() (float64, error) {
	v, r := x.d.GetPowerUsage()
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: get power usage: %w", r)
	}
	return milliWattsToWatts(v), nil
}

func (x realDevice) FanCount() (int, error) {
	v, r := x.d.GetNumFans()
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: get fan count: %w", r)
	}
	return v, nil
}

func (x realDevice) FanSpeedPct(fan int) (float64, error) {
	v, r := x.d.GetFanSpeed_v2(fan)
	if r != nvml.SUCCESS {
		return 0, fmt.Errorf("nvml: get fan %d speed: %w", fan, r)
	}
	return float64(v), nil
}

func (x realDevice) FanControlMode(fan int) (Mode, error) {
	p, r := x.d.GetFanControlPolicy_v2(fan)
	if r != nvml.SUCCESS {
		return ModeAuto, fmt.Errorf("nvml: get fan %d policy: %w", fan, r)
	}
	if p == nvml.FAN_POLICY_MANUAL {
		return ModeManual, nil
	}
	return ModeAuto, nil
}

func (x realDevice) SetFanControlMode(fan int, mode Mode) error {
	var policy nvml.FanControlPolicy = nvml.FAN_POLICY_TEMPERATURE_CONTINOUS_SW
	if mode == ModeManual {
		policy = nvml.FAN_POLICY_MANUAL
	}
	if r := x.d.SetFanControlPolicy(fan, policy); r != nvml.SUCCESS {
		return fmt.Errorf("nvml: set fan %d policy %d: %w", fan, policy, r)
	}
	return nil
}

// SetFanSpeedPct rounds to whole percent: the hardware takes integers.
func (x realDevice) SetFanSpeedPct(fan int, pct float64) error {
	speed := roundSpeedPct(pct)
	if r := x.d.SetFanSpeed_v2(fan, speed); r != nvml.SUCCESS {
		return fmt.Errorf("nvml: set fan %d speed %d: %w", fan, speed, r)
	}
	return nil
}

// roundSpeedPct rounds a fan speed to the whole percent the hardware takes.
func roundSpeedPct(pct float64) int {
	return int(math.Round(pct))
}

// milliWattsToWatts converts NVML's milliwatts to watts.
func milliWattsToWatts(mw uint32) float64 {
	return float64(mw) / 1000
}
