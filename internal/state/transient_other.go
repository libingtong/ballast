//go:build !windows

package state

// isTransientWindows is never consulted off Windows (retryWindows runs op
// once there); no error is transient.
func isTransientWindows(error) bool { return false }
