//go:build unix

package sandbox

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// usernsRestrictedExpected reports whether CI declared that this Linux host
// forbids unprivileged user namespaces (BALLAST_EXPECT_USERNS_RESTRICTED=1,
// set from /proc/sys/kernel/apparmor_restrict_unprivileged_userns, Ubuntu
// 24.04 default). On such a host bwrap cannot configure its network
// namespace ("loopback: Failed RTM_NEWADDR"), so the sandbox must report
// itself unavailable and every consumer must fail closed.
func usernsRestrictedExpected() bool {
	return runtime.GOOS == "linux" && os.Getenv("BALLAST_EXPECT_USERNS_RESTRICTED") == "1"
}

// TestUsernsRestrictedFailClosed is the reverse assertion of the integration
// suite: with BALLAST_EXPECT_USERNS_RESTRICTED=1 the real backend MUST be
// reported unavailable (a Probe that succeeds here means the declaration or
// the probe is wrong, and fails the test). Without the variable it is skipped
// and the usual "missing bwrap fails under CI + BALLAST_REQUIRE_BWRAP"
// semantics of requireBackend apply unchanged.
func TestUsernsRestrictedFailClosed(t *testing.T) {
	if !usernsRestrictedExpected() {
		t.Skip("BALLAST_EXPECT_USERNS_RESTRICTED is not 1")
	}
	// Make sure the failure is the restriction and not a missing bwrap.
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatalf("bwrap is not installed, so unavailability would prove nothing: %v", err)
	}
	kind, err := Probe(Options{})
	if kind != KindNone || err == nil {
		t.Fatalf("Probe = (%q, %v) on a host declared as restricting user namespaces; want (none, error)", kind, err)
	}
	t.Logf("Probe reports the backend unavailable, as expected: %v", err)
}
