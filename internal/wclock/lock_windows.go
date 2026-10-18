//go:build windows

package wclock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
	errorSharingViolation   = syscall.Errno(32)
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func platformPathKey(path string) string {
	// Windows 路径不区分大小写，统一键值可避免盘符大小写等别名绕过锁。
	return strings.ToLower(filepath.Clean(path))
}

func lockFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := procLockFileEx.Call(
		file.Fd(),
		lockFileFailImmediately|lockFileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return nil
	}
	if errors.Is(callErr, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	return callErr
}

func unlockFile(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	result, _, callErr := procUnlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return nil
	}
	if errors.Is(callErr, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	return callErr
}

func isLockBusy(err error) bool {
	return errors.Is(err, errorLockViolation) || errors.Is(err, errorSharingViolation)
}
