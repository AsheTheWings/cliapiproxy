// Package wait provides bounded waits on observable conditions for tests.
//
// Tests wait for a condition to hold instead of sleeping for a fixed duration;
// a wait ends as soon as the condition holds, or fails once its deadline passes.
package wait

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"
)

// pollInterval is how often Poll re-evaluates a condition.
const pollInterval = time.Millisecond

// Poll reports whether cond returned true before timeout elapsed. It evaluates
// cond immediately, then every millisecond, and once more at the deadline.
func Poll(timeout time.Duration, cond func() bool) bool {
	if cond() {
		return true
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return cond()
		case <-ticker.C:
			if cond() {
				return true
			}
		}
	}
}

// Until fails t with the formatted message unless cond returns true before
// timeout elapses.
func Until(t testing.TB, timeout time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	if !Poll(timeout, cond) {
		t.Fatalf(format, args...)
	}
}

// Receive returns the next value from ch, failing t with the formatted message
// when none arrives before timeout elapses.
func Receive[T any](t testing.TB, ch <-chan T, timeout time.Duration, format string, args ...any) T {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		t.Fatalf(format, args...)
		var zero T
		return zero
	}
}

// GoroutineIn reports whether some goroutine's current stack contains every
// one of the given function names, such as a caller parked in a duplicate
// suppression wait. It lets a test wait until concurrent work has reached a
// point that has no other observable effect.
func GoroutineIn(functions ...string) bool {
	return GoroutinesIn(functions...) > 0
}

// GoroutinesIn counts the goroutines whose current stack contains every one
// of the given function names.
func GoroutinesIn(functions ...string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, stack := range bytes.Split(buf, []byte("\n\n")) {
		text := string(stack)
		matched := true
		for _, function := range functions {
			if !strings.Contains(text, function) {
				matched = false
				break
			}
		}
		if matched {
			count++
		}
	}
	return count
}

// SingleflightDuplicate reports whether a caller of a
// golang.org/x/sync/singleflight Group is waiting on another caller's call.
func SingleflightDuplicate() bool {
	return GoroutineIn("singleflight.(*Group).Do(", "sync.(*WaitGroup).Wait(")
}

// settledSamples is how many consecutive stack samples must show no
// runnable goroutine before Settled reports the process settled.
const settledSamples = 3

// Settled waits until no goroutine other than the caller is running or
// runnable in three consecutive samples, so every goroutine is parked on a
// channel, lock, timer, or I/O. A test then checks that an event it forbids
// has not happened: work that could still cause it is blocked. It fails t
// when the process does not settle before timeout elapses.
func Settled(t testing.TB, timeout time.Duration) {
	t.Helper()
	quiet := 0
	if !Poll(timeout, func() bool {
		if busyGoroutines() == 0 {
			quiet++
		} else {
			quiet = 0
		}
		return quiet >= settledSamples
	}) {
		t.Fatalf("goroutines did not settle within %v", timeout)
	}
}

// busyGoroutines counts goroutines, other than the caller, whose stack
// header reports them running, runnable, or in a system call.
func busyGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	busy := 0
	// The first stack is the caller's own goroutine.
	for i, stack := range bytes.Split(buf, []byte("\n\n")) {
		if i == 0 {
			continue
		}
		header, _, _ := bytes.Cut(stack, []byte("\n"))
		open := bytes.IndexByte(header, '[')
		if open < 0 {
			continue
		}
		state := string(header[open+1:])
		if strings.HasPrefix(state, "running") || strings.HasPrefix(state, "runnable") || strings.HasPrefix(state, "syscall") {
			busy++
		}
	}
	return busy
}
