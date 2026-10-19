package wclock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrBusy = errors.New("工作副本正忙")

// Lock 在多个进程之间串行化同一工作副本的写操作。
type Lock struct {
	file *os.File
}

func Acquire(lockDir, workingCopy, operation string) (*Lock, error) {
	absolute, err := canonicalWorkingCopy(workingCopy)
	if err != nil {
		return nil, err
	}
	lock, err := acquireKey(lockDir, platformPathKey(absolute), operation, absolute)
	if errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("%w: %s", ErrBusy, workingCopy)
	}
	return lock, err
}

// 在指定锁目录内按不透明键加锁，目录的不同路径别名共享同一个锁文件。
func AcquireKey(lockDir, key, operation string) (*Lock, error) {
	if key == "" {
		return nil, errors.New("锁键不能为空")
	}
	return acquireKey(lockDir, key, operation, key)
}

func acquireKey(lockDir, key, operation, identity string) (*Lock, error) {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:16]) + ".lock"
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建锁目录失败: %w", err)
	}
	path := filepath.Join(lockDir, name)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开工作副本锁失败: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if isLockBusy(err) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, identity)
		}
		return nil, fmt.Errorf("锁定工作副本失败: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("重置工作副本锁信息失败: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("定位工作副本锁信息失败: %w", err)
	}
	if _, err := fmt.Fprintf(file, "%d\n%s\n%s\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339), operation, identity); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("写入工作副本锁信息失败: %w", err)
	}
	return &Lock{file: file}, nil
}

func canonicalWorkingCopy(workingCopy string) (string, error) {
	absolute, err := filepath.Abs(workingCopy)
	if err != nil {
		return "", fmt.Errorf("解析工作副本路径失败: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = filepath.Clean(resolved)
	}
	return absolute, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("释放工作副本锁失败: %w", err)
	}
	return nil
}
