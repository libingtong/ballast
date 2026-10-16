//go:build unix

package runner

import "syscall"

// openFlagsSource are added to O_RDONLY when a file the agent can write is
// opened for reading: never follow a symlink in the last element and never
// block on a FIFO (docs/p2-design.md §3.4 "job 私有树", source side).
const openFlagsSource = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// openFlagsLeaf is added when a leaf of the private tree is created.
const openFlagsLeaf = syscall.O_NOFOLLOW
