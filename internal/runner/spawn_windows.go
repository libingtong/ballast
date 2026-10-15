//go:build windows

package runner

// spawnDetached starts argv with CreationFlags DETACHED_PROCESS |
// CREATE_NEW_PROCESS_GROUP, stdin NUL, stdout+stderr to logPath.
func spawnDetached(argv []string, env []string, logPath string) (int, error) {
	panic("unimplemented: runner.spawnDetached (windows)")
}
