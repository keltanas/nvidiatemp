package gpu

import (
	"errors"
	"reflect"
	"testing"
)

// Fixed snapshot of real `nvidia-settings -q GPUFanControlState` output from
// the target host (driver 595.x): single GPU currently in manual mode.
const snapshotModesSingle = `Attribute 'GPUFanControlState' (pve:0[gpu:0]): 1.
    'GPUFanControlState' is a boolean attribute; valid values are: 1 (on/true) and 0 (off/false).
    'GPUFanControlState' can use the following target types: GPU.
`

// Two GPUs in the same format: first left in manual mode (e.g. by a
// previous run), second in auto.
const snapshotModesTwo = `Attribute 'GPUFanControlState' (pve:0[gpu:0]): 1.
    'GPUFanControlState' is a boolean attribute; valid values are: 1 (on/true) and 0 (off/false).
    'GPUFanControlState' can use the following target types: GPU.
Attribute 'GPUFanControlState' (pve:0[gpu:1]): 0.
    'GPUFanControlState' is a boolean attribute; valid values are: 1 (on/true) and 0 (off/false).
    'GPUFanControlState' can use the following target types: GPU.
`

func TestParseFanControlStateSingle(t *testing.T) {
	modes, err := parseFanControlState(snapshotModesSingle)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(modes) != 1 || modes[0] != ModeManual {
		t.Errorf("modes = %+v, want {0: manual}", modes)
	}
}

func TestParseFanControlStateTwoGPUs(t *testing.T) {
	modes, err := parseFanControlState(snapshotModesTwo)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if modes[0] != ModeManual || modes[1] != ModeAuto {
		t.Errorf("modes = %+v, want {0: manual, 1: auto}", modes)
	}
}

// A line that looks like an attribute but has no GPU index is not a state
// line; with no valid lines at all the result is ErrNoGPUs.
func TestParseFanControlStateRejectsLineWithoutGPUIndex(t *testing.T) {
	out := "Attribute 'GPUFanControlState' (pve:0): 1.\n"
	if _, err := parseFanControlState(out); !errors.Is(err, ErrNoGPUs) {
		t.Errorf("got %v, want ErrNoGPUs", err)
	}
}

func TestParseFanControlStateRejectsUnknownValue(t *testing.T) {
	out := "Attribute 'GPUFanControlState' (pve:0[gpu:0]): 2.\n"
	if _, err := parseFanControlState(out); err == nil {
		t.Error("unknown state value accepted")
	}
}

func TestParseFanControlStateEmptyIsNoGPUs(t *testing.T) {
	if _, err := parseFanControlState(""); !errors.Is(err, ErrNoGPUs) {
		t.Errorf("empty output: got %v, want ErrNoGPUs", err)
	}
}

// envCapture records the invoked command (name/args/env) and returns a
// fixed output, so both the query and its environment are checkable.
type envCapture struct {
	callCapture
	out string
}

func (e *envCapture) run(name string, args []string, env []string) (string, error) {
	if _, err := e.callCapture.run(name, args, env); err != nil {
		return "", err
	}
	return e.out, nil
}

func TestReadModesIssuesQueryWithDisplay(t *testing.T) {
	c := &envCapture{out: snapshotModesSingle}
	r := &NvidiaModeReader{Run: c.run}
	if _, err := r.ReadModes(); err != nil {
		t.Fatalf("read modes: %v", err)
	}
	if c.name != "nvidia-settings" || !reflect.DeepEqual(c.args, []string{"-q", "GPUFanControlState"}) {
		t.Errorf("command = %q %v, want nvidia-settings -q GPUFanControlState", c.name, c.args)
	}
	displayFromEnv(t, c.env)
}
