package hook

import (
	"testing"
	"time"
)

// The product default does not depend on any build tag or environment.
func TestDefaultWatchdogUnchanged(t *testing.T) {
	if DefaultWatchdog != 150*time.Millisecond {
		t.Errorf("the product default changed: %v", DefaultWatchdog)
	}
	d := &Deps{}
	if d.watchdog() != DefaultWatchdog {
		t.Errorf("a zero Deps.Watchdog must mean the default, got %v", d.watchdog())
	}
}
