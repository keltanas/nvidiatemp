package gpu

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// NvidiaModeReader reads the current fan control state from the hardware
// once at startup (design D3), so a card left in manual mode is picked up
// correctly. Reads work without root, but need the X display.
type NvidiaModeReader struct {
	Run Runner // injectable; nil means the real subprocess runner
}

// NewNvidiaModeReader returns a mode reader backed by real nvidia-settings.
func NewNvidiaModeReader() *NvidiaModeReader {
	return &NvidiaModeReader{}
}

// ReadModes queries GPUFanControlState for all GPUs.
func (r *NvidiaModeReader) ReadModes() (map[int]Mode, error) {
	run := r.Run
	if run == nil {
		run = defaultRunner
	}
	out, err := run("nvidia-settings", []string{"-q", "GPUFanControlState"}, displayEnv())
	if err != nil {
		return nil, fmt.Errorf("nvidia-settings -q GPUFanControlState: %w: %s", err, strings.TrimSpace(out))
	}
	return parseFanControlState(out)
}

// fanControlStateLine matches one attribute line of the real
// nvidia-settings output (verified on the target host, driver 595.x):
//
//	Attribute 'GPUFanControlState' (pve:0[gpu:0]): 1.
var fanControlStateLine = regexp.MustCompile(
	`^Attribute 'GPUFanControlState' \([^)]*\[gpu:(\d+)\]\): (\d+)?\.?$`)

// parseFanControlState parses the fixed nvidia-settings query output:
//
//	Attribute 'GPUFanControlState' (pve:0[gpu:0]): 1.
//	    'GPUFanControlState' is a boolean attribute; valid values are: 1 (on/true) and 0 (off/false).
//	    'GPUFanControlState' can use the following target types: GPU.
//
// with one attribute line per GPU (description lines are ignored).
// 0 = auto, 1 = manual; anything else is an error.
func parseFanControlState(out string) (map[int]Mode, error) {
	modes := make(map[int]Mode)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		m := fanControlStateLine.FindStringSubmatch(line)
		if m == nil {
			continue // description lines and noise
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("malformed GPU index in line %q", line)
		}
		val, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, fmt.Errorf("malformed fan control state in line %q: %w", line, err)
		}
		var mode Mode
		switch val {
		case 0:
			mode = ModeAuto
		case 1:
			mode = ModeManual
		default:
			return nil, fmt.Errorf("unknown fan control state %d in line %q", val, line)
		}
		modes[idx] = mode
	}
	if len(modes) == 0 {
		return nil, ErrNoGPUs
	}
	return modes, nil
}
