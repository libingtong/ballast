package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestUsernsRestrictedDoctorIsT0: with BALLAST_EXPECT_USERNS_RESTRICTED=1 (the
// host forbids unprivileged user namespaces, so the runner sandbox cannot
// work) `ballast doctor` must judge the host T0 and say the sandbox backend is
// unavailable. Skipped when the variable is not set.
func TestUsernsRestrictedDoctorIsT0(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("BALLAST_EXPECT_USERNS_RESTRICTED") != "1" {
		t.Skip("BALLAST_EXPECT_USERNS_RESTRICTED is not 1")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Fatalf("bwrap is not installed, so unavailability would prove nothing: %v", err)
	}
	s := newSandbox(t)
	r := s.mustExit(0, "", "", "doctor", "--json")
	var doc struct {
		Tier   string `json:"tier"`
		Checks []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Tier != "T0" {
		t.Errorf("tier = %q, want T0", doc.Tier)
	}
	found := false
	for _, c := range doc.Checks {
		if c.ID != "tier.runner_sandbox" {
			continue
		}
		found = true
		if !strings.Contains(c.Message, "当前后端不可用") {
			t.Errorf("tier.runner_sandbox does not report the backend as unavailable: %+v", c)
		}
	}
	if !found {
		t.Errorf("no tier.runner_sandbox check in %s", r.stdout)
	}
}
