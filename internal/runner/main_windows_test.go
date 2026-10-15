//go:build windows

package runner

import (
	"fmt"
	"os"
)

func selfKill() { os.Exit(9) }

func childInfo() string { return fmt.Sprintf("%d 0", os.Getpid()) }
