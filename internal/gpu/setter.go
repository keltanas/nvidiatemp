package gpu

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// NvidiaSetter writes fan control state via nvidia-settings (design D4):
// mode + speed in one call, return-to-auto in a separate call. When euid
// is not 0 the command is wrapped in sudo; DISPLAY=:0 is always passed.
type NvidiaSetter struct {
	Run  Runner // injectable; nil means the real subprocess runner
	Euid int    // effective uid; tests inject it, production uses os.Geteuid()
}

// NewNvidiaSetter returns a setter backed by real nvidia-settings calls.
func NewNvidiaSetter() *NvidiaSetter {
	return &NvidiaSetter{Euid: os.Geteuid()}
}

// SetManual switches the GPU to manual mode and sets the target fan speed
// in a single nvidia-settings call. The speed is rounded to an integer —
// the hardware takes whole percentages.
func (s *NvidiaSetter) SetManual(index int, speedPct float64) error {
	speed := int(math.Round(speedPct))
	args := []string{
		"-a", fmt.Sprintf("[gpu:%d]/GPUFanControlState=1", index),
		"-a", fmt.Sprintf("[fan:%d]/GPUTargetFanSpeed=%d", index, speed),
	}
	return s.exec(args)
}

// SetAuto returns the GPU to driver-controlled (auto) mode.
func (s *NvidiaSetter) SetAuto(index int) error {
	args := []string{"-a", fmt.Sprintf("[gpu:%d]/GPUFanControlState=0", index)}
	return s.exec(args)
}

// exec runs nvidia-settings with the sudo prefix and DISPLAY=:0.
func (s *NvidiaSetter) exec(args []string) error {
	run := s.Run
	if run == nil {
		run = defaultRunner
	}
	euid := s.Euid
	if euid < 0 {
		euid = os.Geteuid()
	}
	name, fullArgs := setterCommand("nvidia-settings", args, euid)
	out, err := run(name, fullArgs, displayEnv())
	if err != nil {
		return fmt.Errorf("nvidia-settings: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// setterCommand builds the argv for a nvidia-settings write. When euid is
// not 0 it wraps the command in sudo; DISPLAY=:0 is passed to sudo as a
// VAR=val argument so it survives sudo's env_reset (the exact chain
// verified on the target host: sudo DISPLAY=:0 nvidia-settings ...).
func setterCommand(binary string, args []string, euid int) (string, []string) {
	if euid == 0 {
		return binary, args
	}
	full := make([]string, 0, len(args)+2)
	full = append(full, "DISPLAY=:0", binary)
	full = append(full, args...)
	return "sudo", full
}
