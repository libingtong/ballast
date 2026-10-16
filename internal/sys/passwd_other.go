//go:build !darwin && !linux

package sys

import "os"

func euid() int { return os.Geteuid() }

func lookupHome(uid int) (string, error) { return "", ErrPasswdUnsupported }
