//go:build unix

package main

import "syscall"

func lockExclusive(fd uintptr) error {
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			return err
		}
	}
}
