//go:build unix

package runner

// spawnDetached starts argv with Setsid, stdin /dev/null, stdout+stderr to
// logPath (O_APPEND|O_CREATE, 0600; "" ⇒ /dev/null), then Release()s it.
func spawnDetached(argv []string, env []string, logPath string) (int, error) {
	panic("unimplemented: runner.spawnDetached (unix)")
}
