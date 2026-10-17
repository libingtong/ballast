package sys

import (
	"errors"
	"testing"
	"time"
)

var errBusy = errors.New("busy")

func TestRetryTransientSucceedsAfterCollisions(t *testing.T) {
	calls := 0
	err := retryTransient(time.Second, func(e error) bool { return e == errBusy }, func() error {
		calls++
		if calls < 4 {
			return errBusy
		}
		return nil
	})
	if err != nil || calls != 4 {
		t.Fatalf("err=%v calls=%d, want nil and 4", err, calls)
	}
}

func TestRetryTransientStopsOnOtherErrors(t *testing.T) {
	other := errors.New("other")
	calls := 0
	err := retryTransient(time.Second, func(e error) bool { return e == errBusy }, func() error {
		calls++
		return other
	})
	if err != other || calls != 1 {
		t.Fatalf("err=%v calls=%d, want the error after one call", err, calls)
	}
}

func TestRetryTransientIsBounded(t *testing.T) {
	start := time.Now()
	err := retryTransient(10*time.Millisecond, func(error) bool { return true }, func() error { return errBusy })
	if err != errBusy {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("retry ran for %v, want about the 10ms budget", d)
	}
}
