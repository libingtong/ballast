//go:build !windows

package config

func isInvalidPath(error) bool { return false }
