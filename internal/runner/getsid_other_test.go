//go:build !darwin && !linux

package runner

func getsidSelf() int { return -1 }
