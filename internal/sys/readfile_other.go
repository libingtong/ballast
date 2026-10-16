//go:build !windows

package sys

import "os"

func readFile(name string) ([]byte, error) { return os.ReadFile(name) }
