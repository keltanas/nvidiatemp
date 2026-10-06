package gpu

import "fmt"

// BackendKind selects how fan control reaches the driver.
type BackendKind string

const (
	// BackendAuto prefers NVML and falls back to nvidia-settings when NVML
	// is unavailable or its fan control is not writable.
	BackendAuto BackendKind = "auto"
	// BackendNVML uses NVML only; startup fails when it is unavailable.
	BackendNVML BackendKind = "nvml"
	// BackendSettings uses nvidia-settings and therefore needs an X display.
	BackendSettings BackendKind = "nvidia-settings"
)

// ParseBackendKind validates a configured backend name; an empty string means
// BackendAuto.
func ParseBackendKind(s string) (BackendKind, error) {
	switch BackendKind(s) {
	case "", BackendAuto:
		return BackendAuto, nil
	case BackendNVML:
		return BackendNVML, nil
	case BackendSettings:
		return BackendSettings, nil
	default:
		return "", fmt.Errorf("unknown backend %q: want auto, nvml or nvidia-settings", s)
	}
}

// Backend bundles the three GPU seams for one backend implementation.
type Backend struct {
	Kind   BackendKind
	Reader Reader
	Setter FanSetter
	Modes  ModeReader
	// Fallback is non-nil when auto was requested and NVML was unavailable,
	// so the caller can log why nvidia-settings was chosen instead.
	Fallback error
	close    func()
}

// Close releases backend resources.
func (b *Backend) Close() {
	if b != nil && b.close != nil {
		b.close()
	}
}

// openNVML is a seam for tests; production initializes the real library.
var openNVML = func() (*NVML, error) { return NewNVML() }

// Open builds the requested backend for fan control. BackendAuto prefers NVML
// and falls back to nvidia-settings when NVML is unavailable or its fan
// control is read-only, so a pre-existing nvidia-settings setup keeps working
// unchanged.
func Open(kind BackendKind) (*Backend, error) { return open(kind, true) }

// OpenReadOnly builds a backend for commands that only read state (status).
// It skips the NVML write probe, which needs root, so an unprivileged
// `nvidiatemp status` still reports through NVML.
func OpenReadOnly(kind BackendKind) (*Backend, error) { return open(kind, false) }

func open(kind BackendKind, probeWrites bool) (*Backend, error) {
	switch kind {
	case "", BackendAuto:
		b, err := openNVMLBackend(probeWrites)
		if err != nil {
			sb := settingsBackend()
			sb.Fallback = err
			return sb, nil
		}
		return b, nil
	case BackendNVML:
		return openNVMLBackend(probeWrites)
	case BackendSettings:
		return settingsBackend(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q: want auto, nvml or nvidia-settings", kind)
	}
}

// openNVMLBackend initializes NVML and, for fan control, verifies that the
// write path works before the daemon starts managing anything.
func openNVMLBackend(probeWrites bool) (*Backend, error) {
	n, err := openNVML()
	if err != nil {
		return nil, err
	}
	if probeWrites {
		if err := n.CheckWritable(); err != nil {
			n.Close()
			return nil, err
		}
	}
	return &Backend{Kind: BackendNVML, Reader: n, Setter: n, Modes: n, close: n.Close}, nil
}

// settingsBackend returns the nvidia-settings (X display) backend.
func settingsBackend() *Backend {
	return &Backend{
		Kind:   BackendSettings,
		Reader: NewNvidiaReader(),
		Setter: NewNvidiaSetter(),
		Modes:  NewNvidiaModeReader(),
	}
}
