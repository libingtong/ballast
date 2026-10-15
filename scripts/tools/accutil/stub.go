package main

import (
	"os"
	"strconv"
	"strings"
)

// runStub behaves as a stub build tool: it logs how it was called and exits
// with the status requested through the environment.
func runStub(name string, args []string) int {
	if p := os.Getenv("ACC_STUB_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(name + " " + strings.Join(args, " ") + "\n")
			_ = f.Close()
		}
	}
	if v := os.Getenv("ACC_STUB_EXIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
