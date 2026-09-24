// Package gpu abstracts access to NVIDIA GPUs behind two interfaces —
// Reader (temperature, power, current fan speed) and FanSetter (mode,
// speed) — with a subprocess backend: nvidia-smi for reads,
// nvidia-settings for writes (design D1).
package gpu

// Info is one GPU snapshot from a single poll.
type Info struct {
	Index       int
	Name        string
	TempC       float64 // °C
	PowerW      float64 // W
	FanSpeedPct float64 // % (as reported by the driver; may exceed 100)
}

// Mode is the fan control state of a GPU.
type Mode int

const (
	ModeAuto   Mode = iota // driver controls the fan
	ModeManual             // nvidiatemp sets the fan speed
)

// String returns "auto" or "manual".
func (m Mode) String() string {
	if m == ModeManual {
		return "manual"
	}
	return "auto"
}

// Reader provides one poll of all visible GPUs.
type Reader interface {
	Read() ([]Info, error)
}

// FanSetter writes fan control state to the hardware. SetManual switches
// the GPU to manual mode and sets the speed in one call; SetAuto returns
// the GPU to driver control.
type FanSetter interface {
	SetManual(index int, speedPct float64) error
	SetAuto(index int) error
}

// ModeReader reads the current fan control state from the hardware, used
// once at startup to initialize internal state (design D3).
type ModeReader interface {
	ReadModes() (map[int]Mode, error)
}
