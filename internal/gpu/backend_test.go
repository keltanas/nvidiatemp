package gpu

import (
	"errors"
	"strings"
	"testing"
)

// withOpenNVML replaces the NVML factory seam for one test.
func withOpenNVML(t *testing.T, fn func() (*NVML, error)) {
	t.Helper()
	prev := openNVML
	openNVML = fn
	t.Cleanup(func() { openNVML = prev })
}

func TestParseBackendKind(t *testing.T) {
	cases := []struct {
		in      string
		want    BackendKind
		wantErr bool
	}{
		{"", BackendAuto, false},
		{"auto", BackendAuto, false},
		{"nvml", BackendNVML, false},
		{"nvidia-settings", BackendSettings, false},
		{"NVML", "", true},
		{"nvidia_settings", "", true},
		{"nvidia-smi", "", true},
	}
	for _, c := range cases {
		got, err := ParseBackendKind(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseBackendKind(%q) = %q, want an error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseBackendKind(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func nvmlTestBackend() (*NVML, *fakeNvmlAPI) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{
		{index: 0, speeds: []float64{30}, policies: []Mode{ModeAuto}},
	}}
	return newTestNVML(api), api
}

// An explicit nvml backend is used as-is when it initializes and is writable.
func TestOpenExplicitNVML(t *testing.T) {
	nv, api := nvmlTestBackend()
	withOpenNVML(t, func() (*NVML, error) { return nv, nil })

	b, err := Open(BackendNVML)
	if err != nil {
		t.Fatalf("Open(nvml): %v", err)
	}
	if b.Kind != BackendNVML {
		t.Errorf("kind = %q, want nvml", b.Kind)
	}
	if b.Reader == nil || b.Setter == nil || b.Modes == nil {
		t.Fatal("backend seams must all be set")
	}
	b.Close()
	if !api.shutdown {
		t.Error("Close did not release NVML")
	}
}

// An explicit nvml backend fails fast when NVML cannot be initialized: the
// operator asked for it, so a silent fallback would hide the problem.
func TestOpenExplicitNVMLFailsFast(t *testing.T) {
	withOpenNVML(t, func() (*NVML, error) { return nil, errors.New("libnvidia-ml not found") })
	if _, err := Open(BackendNVML); err == nil {
		t.Fatal("Open(nvml) = nil error, want a failure")
	}
}

// auto prefers NVML when it works.
func TestOpenAutoUsesNVML(t *testing.T) {
	nv, _ := nvmlTestBackend()
	withOpenNVML(t, func() (*NVML, error) { return nv, nil })

	b, err := Open(BackendAuto)
	if err != nil {
		t.Fatalf("Open(auto): %v", err)
	}
	if b.Kind != BackendNVML {
		t.Errorf("kind = %q, want nvml", b.Kind)
	}
	if b.Fallback != nil {
		t.Errorf("Fallback = %v, want nil", b.Fallback)
	}
}

// auto falls back to nvidia-settings when NVML is not available, recording
// why so the daemon can log it.
func TestOpenAutoFallsBackWhenNVMLUnavailable(t *testing.T) {
	withOpenNVML(t, func() (*NVML, error) { return nil, errors.New("libnvidia-ml not found") })

	b, err := Open(BackendAuto)
	if err != nil {
		t.Fatalf("Open(auto): %v", err)
	}
	if b.Kind != BackendSettings {
		t.Errorf("kind = %q, want nvidia-settings", b.Kind)
	}
	if b.Fallback == nil || !strings.Contains(b.Fallback.Error(), "libnvidia-ml") {
		t.Errorf("Fallback = %v, want the NVML error", b.Fallback)
	}
}

// auto also falls back when NVML initializes but fan control is read-only,
// and releases the library it opened.
func TestOpenAutoFallsBackWhenNotWritable(t *testing.T) {
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{{
		index: 0, speeds: []float64{30}, policies: []Mode{ModeAuto},
		writeFail: errors.New("NVML_ERROR_NOT_SUPPORTED"),
	}}}
	withOpenNVML(t, func() (*NVML, error) { return newTestNVML(api), nil })

	b, err := Open(BackendAuto)
	if err != nil {
		t.Fatalf("Open(auto): %v", err)
	}
	if b.Kind != BackendSettings {
		t.Errorf("kind = %q, want nvidia-settings", b.Kind)
	}
	if b.Fallback == nil {
		t.Error("Fallback must record the failed NVML write probe")
	}
	if !api.shutdown {
		t.Error("the rejected NVML backend must be closed")
	}
}

// The nvidia-settings backend needs no probe.
func TestOpenSettings(t *testing.T) {
	b, err := Open(BackendSettings)
	if err != nil {
		t.Fatalf("Open(nvidia-settings): %v", err)
	}
	if b.Kind != BackendSettings || b.Fallback != nil {
		t.Errorf("backend = %+v, want plain nvidia-settings", b)
	}
}

// A read-only open skips the write probe, which needs root: an unprivileged
// `nvidiatemp status` still uses NVML reads instead of falling back to X.
func TestOpenReadOnlySkipsWriteProbe(t *testing.T) {
	dev := &fakeNvmlDevice{
		index: 0, speeds: []float64{30}, policies: []Mode{ModeAuto},
		writeFail: errors.New("NVML_ERROR_NO_PERMISSION"),
	}
	api := &fakeNvmlAPI{devices: []*fakeNvmlDevice{dev}}
	withOpenNVML(t, func() (*NVML, error) { return newTestNVML(api), nil })

	b, err := OpenReadOnly(BackendAuto)
	if err != nil {
		t.Fatalf("OpenReadOnly(auto): %v", err)
	}
	if b.Kind != BackendNVML {
		t.Errorf("kind = %q, want nvml", b.Kind)
	}
	if b.Fallback != nil {
		t.Errorf("Fallback = %v, want nil", b.Fallback)
	}
	if len(dev.setPolicies) != 0 {
		t.Errorf("read-only open wrote policies: %v", dev.setPolicies)
	}
}

// An empty kind means auto.
func TestOpenEmptyKindIsAuto(t *testing.T) {
	nv, _ := nvmlTestBackend()
	withOpenNVML(t, func() (*NVML, error) { return nv, nil })

	b, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\"): %v", err)
	}
	if b.Kind != BackendNVML {
		t.Errorf("kind = %q, want nvml (auto)", b.Kind)
	}
}

func TestOpenUnknownKind(t *testing.T) {
	if _, err := Open(BackendKind("bogus")); err == nil {
		t.Fatal("Open(bogus) = nil error, want a failure")
	}
}
