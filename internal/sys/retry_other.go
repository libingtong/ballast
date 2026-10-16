//go:build !windows

package sys

func isTransientRead(error) bool   { return false }
func isTransientRename(error) bool { return false }
