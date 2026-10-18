//go:build !windows

package safepoint

import "syscall"

func makeTestFIFO(path string) error { return syscall.Mkfifo(path, 0o600) }
