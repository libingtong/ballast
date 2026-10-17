//go:build ballast_test

package cli_test

// childTagNames are the build tags of the ballast binary the e2e tests exec:
// the same as the test binary's own, so CI's `go test -tags ballast_test`
// exercises a binary that honours BALLAST_TEST_WATCHDOG_MS.
func childTagNames() []string { return []string{"ballast_test"} }
