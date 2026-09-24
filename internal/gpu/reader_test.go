package gpu

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fixedOutput is a capture stub: it records the invoked command and returns
// a fixed snapshot instead of running a real subprocess.
type fixedOutput struct {
	out   string
	err   error
	calls []string // "name arg1 arg2 ..." per call
}

func (f *fixedOutput) run(name string, args []string, _ []string) (string, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	return f.out, f.err
}

// Fixed snapshot of real nvidia-smi output from the target machine
// (single RTX 4080, csv,noheader,nounits) — captured live from the host.
const snapshotSingleGPU = `0, NVIDIA GeForce RTX 4080, 53, 273.43, 60`

// Fixed snapshot: two GPUs in the same format (synthesized — the target
// host has a single GPU), fractional power.draw.
const snapshotTwoGPUs = `0, NVIDIA GeForce RTX 4080, 67, 287.34, 72
1, NVIDIA GeForce RTX 4080, 61, 12.50, 35`

func TestParseSMISingleGPU(t *testing.T) {
	infos, err := parseSMIOutput(snapshotSingleGPU)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d GPUs, want 1", len(infos))
	}
	got := infos[0]
	want := Info{Index: 0, Name: "NVIDIA GeForce RTX 4080", TempC: 53, PowerW: 273.43, FanSpeedPct: 60}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseSMITwoGPUs(t *testing.T) {
	infos, err := parseSMIOutput(snapshotTwoGPUs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d GPUs, want 2", len(infos))
	}
	if infos[0].Index != 0 || infos[1].Index != 1 {
		t.Errorf("indices = %d, %d; want 0, 1", infos[0].Index, infos[1].Index)
	}
	if infos[0].PowerW != 287.34 {
		t.Errorf("power = %v, want 287.34 (fractional)", infos[0].PowerW)
	}
	if infos[1].FanSpeedPct != 35 {
		t.Errorf("fan speed = %v, want 35", infos[1].FanSpeedPct)
	}
}

// fan.speed may exceed 100 — accepted as-is (design D2).
func TestParseSMIFanSpeedAbove100(t *testing.T) {
	infos, err := parseSMIOutput("0, NVIDIA GeForce RTX 4080, 70, 250.1, 105")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if infos[0].FanSpeedPct != 105 {
		t.Errorf("fan speed = %v, want 105", infos[0].FanSpeedPct)
	}
}

// A GPU name containing commas must not break field alignment.
func TestParseSMINameWithComma(t *testing.T) {
	infos, err := parseSMIOutput("0, NVIDIA Some, Card X, 55, 31.23, 40")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if infos[0].Name != "NVIDIA Some, Card X" {
		t.Errorf("name = %q, want %q", infos[0].Name, "NVIDIA Some, Card X")
	}
	if infos[0].TempC != 55 || infos[0].PowerW != 31.23 || infos[0].FanSpeedPct != 40 {
		t.Errorf("numeric fields misaligned: %+v", infos[0])
	}
}

// Strict parsing: any non-numeric field is a poll error (design D2).
func TestParseSMIRejectsNAFields(t *testing.T) {
	for _, line := range []string{
		"0, NVIDIA GeForce RTX 4080, [N/A], 31.23, 40",
		"0, NVIDIA GeForce RTX 4080, 55, [N/A], 40",
		"0, NVIDIA GeForce RTX 4080, 55, 31.23, [N/A]",
	} {
		if _, err := parseSMIOutput(line); err == nil {
			t.Errorf("line %q accepted, want poll error", line)
		}
	}
}

func TestParseSMIRejectsMalformedLine(t *testing.T) {
	if _, err := parseSMIOutput("0, NVIDIA GeForce RTX 4080, 55"); err == nil {
		t.Error("too few fields accepted")
	}
	if _, err := parseSMIOutput("x, NVIDIA GeForce RTX 4080, 55, 31.23, 40"); err == nil {
		t.Error("non-numeric index accepted")
	}
}

func TestParseSMIEmptyOutputIsNoGPUs(t *testing.T) {
	if _, err := parseSMIOutput(""); !errors.Is(err, ErrNoGPUs) {
		t.Errorf("empty output: got %v, want ErrNoGPUs", err)
	}
}

// Read() issues exactly the fixed query for all GPUs.
func TestReaderIssuesSingleQuery(t *testing.T) {
	f := &fixedOutput{out: snapshotTwoGPUs}
	r := &NvidiaReader{Run: f.run}
	if _, err := r.Read(); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(f.calls))
	}
	want := "nvidia-smi --query-gpu=index,name,temperature.gpu,power.draw,fan.speed --format=csv,noheader,nounits"
	if f.calls[0] != want {
		t.Errorf("call = %q, want %q", f.calls[0], want)
	}
}

// nvidia-smi with no GPUs reports «No devices were found» and exits 9.
func TestReaderNoDevicesIsErrNoGPUs(t *testing.T) {
	f := &fixedOutput{out: "No devices were found.\n", err: fmt.Errorf("exit status 9")}
	r := &NvidiaReader{Run: f.run}
	if _, err := r.Read(); !errors.Is(err, ErrNoGPUs) {
		t.Errorf("got %v, want ErrNoGPUs", err)
	}
}

func TestReaderExecFailureIsPollError(t *testing.T) {
	f := &fixedOutput{out: "NVIDIA-SMI has failed because it could not communicate with the NVIDIA driver.", err: fmt.Errorf("exit status 9")}
	r := &NvidiaReader{Run: f.run}
	_, err := r.Read()
	if err == nil || errors.Is(err, ErrNoGPUs) {
		t.Errorf("driver failure must be a plain poll error, got %v", err)
	}
}
