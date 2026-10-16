//go:build !unix

package runner

import "testing"

func mkfifo(t *testing.T, path string) { t.Skip("no FIFOs on this platform") }
