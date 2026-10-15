//go:build !(linux || darwin || freebsd || windows)

package install

func diskFree(dir string) (uint64, bool) { return 0, false }
