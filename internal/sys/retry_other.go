//go:build !windows

package sys

func isTransientRead(error) bool           { return false }
func isTransientRename(string, error) bool { return false }
