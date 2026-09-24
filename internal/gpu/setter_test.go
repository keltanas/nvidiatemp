package gpu

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// callCapture records the exact command a setter would run, without
// executing it.
type callCapture struct {
	name string
	args []string
	env  []string
}

func (c *callCapture) run(name string, args []string, env []string) (string, error) {
	c.name = name
	c.args = append([]string(nil), args...)
	c.env = append([]string(nil), env...)
	return "", nil
}

func displayFromEnv(t *testing.T, env []string) string {
	t.Helper()
	var displays []string
	for _, e := range env {
		if strings.HasPrefix(e, "DISPLAY=") {
			displays = append(displays, e)
		}
	}
	if len(displays) != 1 || displays[0] != "DISPLAY=:0" {
		t.Fatalf("env DISPLAY entries = %v, want exactly [DISPLAY=:0]", displays)
	}
	return displays[0]
}

// Running as root: no sudo prefix.
func TestSetManualAsRoot(t *testing.T) {
	c := &callCapture{}
	s := &NvidiaSetter{Run: c.run, Euid: 0}
	if err := s.SetManual(0, 42); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	if c.name != "nvidia-settings" {
		t.Errorf("name = %q, want nvidia-settings (no sudo as root)", c.name)
	}
	want := []string{
		"-a", "[gpu:0]/GPUFanControlState=1",
		"-a", "[fan:0]/GPUTargetFanSpeed=42",
	}
	if !reflect.DeepEqual(c.args, want) {
		t.Errorf("args = %v, want %v", c.args, want)
	}
	displayFromEnv(t, c.env)
}

// Running as a regular user: sudo prefix with DISPLAY=:0 passed through.
func TestSetManualAsUserUsesSudo(t *testing.T) {
	c := &callCapture{}
	s := &NvidiaSetter{Run: c.run, Euid: 1000}
	if err := s.SetManual(1, 55); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	if c.name != "sudo" {
		t.Errorf("name = %q, want sudo for euid=1000", c.name)
	}
	want := []string{
		"DISPLAY=:0", "nvidia-settings",
		"-a", "[gpu:1]/GPUFanControlState=1",
		"-a", "[fan:1]/GPUTargetFanSpeed=55",
	}
	if !reflect.DeepEqual(c.args, want) {
		t.Errorf("args = %v, want %v", c.args, want)
	}
	displayFromEnv(t, c.env)
}

// Mode and speed go in one call (design D4).
func TestSetManualModeAndSpeedInOneCall(t *testing.T) {
	c := &callCapture{}
	s := &NvidiaSetter{Run: c.run, Euid: 0}
	if err := s.SetManual(0, 42); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	joined := strings.Join(c.args, " ")
	if !strings.Contains(joined, "GPUFanControlState=1") || !strings.Contains(joined, "GPUTargetFanSpeed=") {
		t.Errorf("mode and speed must be in one call, got %q", joined)
	}
}

// Return to auto is a separate call touching only the control state.
func TestSetAuto(t *testing.T) {
	c := &callCapture{}
	s := &NvidiaSetter{Run: c.run, Euid: 0}
	if err := s.SetAuto(2); err != nil {
		t.Fatalf("set auto: %v", err)
	}
	want := []string{"-a", "[gpu:2]/GPUFanControlState=0"}
	if !reflect.DeepEqual(c.args, want) {
		t.Errorf("args = %v, want %v", c.args, want)
	}
	if strings.Contains(strings.Join(c.args, " "), "GPUTargetFanSpeed") {
		t.Error("auto call must not touch the target speed")
	}
}

// The hardware takes whole percentages: fractional targets are rounded.
func TestSetManualRoundsSpeed(t *testing.T) {
	for in, want := range map[float64]string{42.4: "42", 42.5: "43", 42.6: "43", 100: "100"} {
		c := &callCapture{}
		s := &NvidiaSetter{Run: c.run, Euid: 0}
		if err := s.SetManual(0, in); err != nil {
			t.Fatalf("set manual %v: %v", in, err)
		}
		if !strings.Contains(strings.Join(c.args, " "), "GPUTargetFanSpeed="+want) {
			t.Errorf("speed %v → args %v, want GPUTargetFanSpeed=%s", in, c.args, want)
		}
	}
}

// A failing write surfaces as an error with the command output.
func TestSetManualExecFailure(t *testing.T) {
	f := &fixedOutput{out: "ERROR: Unable to contact X server.", err: errors.New("exit status 1")}
	s := &NvidiaSetter{Run: f.run, Euid: 0}
	if err := s.SetManual(0, 42); err == nil {
		t.Error("exec failure must be an error")
	}
}
