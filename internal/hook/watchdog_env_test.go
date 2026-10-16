package hook

import (
	"testing"
	"time"
)

func TestWatchdogFromEnv(t *testing.T) {
	tests := []struct {
		name string
		val  string
		set  bool
		want time.Duration
	}{
		{"unset keeps the product default", "", false, 0},
		{"empty is ignored", "", true, 0},
		{"relaxed budget", "3000", true, 3 * time.Second},
		{"surrounding spaces", " 500 ", true, 500 * time.Millisecond},
		{"exactly the default", "150", true, 150 * time.Millisecond},
		{"tighter than the default is ignored", "20", true, 0},
		{"zero is ignored", "0", true, 0},
		{"negative is ignored", "-5", true, 0},
		{"not a number", "soon", true, 0},
		{"fraction is ignored", "1.5", true, 0},
		{"above one minute is ignored", "60001", true, 0},
		{"one minute", "60000", true, time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WatchdogFromEnv(func(k string) (string, bool) {
				if k != WatchdogEnv {
					t.Fatalf("looked up %q", k)
				}
				return tc.val, tc.set
			})
			if got != tc.want {
				t.Errorf("WatchdogFromEnv(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
	if DefaultWatchdog != 150*time.Millisecond {
		t.Errorf("the product default changed: %v", DefaultWatchdog)
	}
}
