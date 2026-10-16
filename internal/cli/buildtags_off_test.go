//go:build !ballast_test

package cli_test

// childBuildTags: the e2e tests exec the plain production binary.
func childBuildTags() []string { return nil }
