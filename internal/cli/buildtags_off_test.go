//go:build !ballast_test

package cli_test

// childTagNames: the e2e tests exec the plain production binary.
func childTagNames() []string { return nil }
