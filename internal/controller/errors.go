package controller

// DefaultMaxConsecutiveFailures is the number of consecutive failed polls
// after which the daemon restores auto mode on all GPUs and exits with a
// non-zero code (spec: fan-control, «Устойчивая ошибка опроса»).
const DefaultMaxConsecutiveFailures = 5

// FailureTracker counts consecutive failed polls.
type FailureTracker struct {
	MaxConsecutive int
	count          int
}

// NewFailureTracker returns a tracker with the default threshold.
func NewFailureTracker() *FailureTracker {
	return &FailureTracker{MaxConsecutive: DefaultMaxConsecutiveFailures}
}

// Record records one poll outcome and reports whether the consecutive
// failure threshold has been reached (a sustained poll error: the daemon
// must restore auto mode on all GPUs and exit with a non-zero code).
func (f *FailureTracker) Record(ok bool) bool {
	if ok {
		f.count = 0
		return false
	}
	f.count++
	return f.count >= f.MaxConsecutive
}
