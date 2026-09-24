package controller

import "testing"

// Spec scenario «Устойчивая ошибка опроса»: 5 consecutive failed polls →
// the threshold is reached (restore auto on all GPUs + exit non-zero).
func TestFailureTrackerThreshold(t *testing.T) {
	f := NewFailureTracker()
	if f.MaxConsecutive != 5 {
		t.Fatalf("default threshold = %d, want 5", f.MaxConsecutive)
	}
	for i := 1; i <= 4; i++ {
		if f.Record(false) {
			t.Fatalf("threshold reached after only %d failures", i)
		}
	}
	if !f.Record(false) {
		t.Fatal("threshold not reached after 5 consecutive failures")
	}
}

// A successful poll resets the consecutive counter.
func TestFailureTrackerResetsOnSuccess(t *testing.T) {
	f := NewFailureTracker()
	for i := 0; i < 4; i++ {
		f.Record(false)
	}
	f.Record(true) // driver recovers
	for i := 1; i <= 4; i++ {
		if f.Record(false) {
			t.Fatalf("threshold reached after success + %d failures", i)
		}
	}
	if !f.Record(false) {
		t.Fatal("5 consecutive failures after a reset must reach the threshold")
	}
}
