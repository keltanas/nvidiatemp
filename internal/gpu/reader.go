package gpu

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrNoGPUs indicates that no visible NVIDIA GPU was found.
var ErrNoGPUs = errors.New("no visible NVIDIA GPUs")

// smiQueryArgs is the single per-poll nvidia-smi call covering all GPUs
// (design D2): one process, strict CSV without headers and units.
var smiQueryArgs = []string{
	"--query-gpu=index,name,temperature.gpu,power.draw,fan.speed",
	"--format=csv,noheader,nounits",
}

// NvidiaReader polls all visible GPUs with one nvidia-smi call per Read.
type NvidiaReader struct {
	Run Runner // injectable; nil means the real subprocess runner
}

// NewNvidiaReader returns a reader backed by real nvidia-smi calls.
func NewNvidiaReader() *NvidiaReader {
	return &NvidiaReader{}
}

// Read polls all visible GPUs once. Any parse problem is a poll error
// (design D2) and feeds the consecutive-failure policy.
func (r *NvidiaReader) Read() ([]Info, error) {
	run := r.Run
	if run == nil {
		run = defaultRunner
	}
	out, err := run("nvidia-smi", smiQueryArgs, os.Environ())
	if err != nil {
		if strings.Contains(out, "No devices were found") {
			return nil, ErrNoGPUs
		}
		return nil, fmt.Errorf("nvidia-smi: %w: %s", err, strings.TrimSpace(out))
	}
	return parseSMIOutput(out)
}

// parseSMIOutput parses the fixed CSV snapshot of nvidia-smi output.
// Line format: index,name,temperature.gpu,power.draw,fan.speed — a name
// containing commas is preserved by joining the middle fields back.
func parseSMIOutput(out string) ([]Info, error) {
	var infos []Info
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		info, err := parseSMILine(line)
		if err != nil {
			return nil, err // strict: a malformed line is a poll error
		}
		infos = append(infos, info)
	}
	if len(infos) == 0 {
		return nil, ErrNoGPUs
	}
	return infos, nil
}

// parseSMILine parses one CSV line with strict float parsing: power is
// fractional, fan speed may exceed 100 and is accepted as-is (design D2).
func parseSMILine(line string) (Info, error) {
	parts := strings.Split(line, ",")
	if len(parts) < 5 {
		return Info{}, fmt.Errorf("malformed nvidia-smi line %q: want 5 fields", line)
	}
	last := len(parts) - 1
	index, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return Info{}, fmt.Errorf("malformed GPU index in line %q: %w", line, err)
	}
	temp, err := strconv.ParseFloat(strings.TrimSpace(parts[last-2]), 64)
	if err != nil {
		return Info{}, fmt.Errorf("malformed temperature in line %q: %w", line, err)
	}
	power, err := strconv.ParseFloat(strings.TrimSpace(parts[last-1]), 64)
	if err != nil {
		return Info{}, fmt.Errorf("malformed power in line %q: %w", line, err)
	}
	fan, err := strconv.ParseFloat(strings.TrimSpace(parts[last]), 64)
	if err != nil {
		return Info{}, fmt.Errorf("malformed fan speed in line %q: %w", line, err)
	}
	// The CSV separator is ", "; rejoin trimmed middle fields with the
	// same separator to preserve names that contain commas.
	fields := make([]string, 0, len(parts)-3)
	for _, p := range parts[1 : last-2] {
		fields = append(fields, strings.TrimSpace(p))
	}
	name := strings.Join(fields, ", ")
	return Info{
		Index:       index,
		Name:        name,
		TempC:       temp,
		PowerW:      power,
		FanSpeedPct: fan,
	}, nil
}
