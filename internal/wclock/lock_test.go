package wclock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireSerializesAliasesAndReleases(t *testing.T) {
	root := t.TempDir()
	workingCopy := filepath.Join(root, "wc")
	if err := mkdirAll(workingCopy); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := symlink(workingCopy, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lockDir := filepath.Join(root, "locks")
	first, err := Acquire(lockDir, workingCopy, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(lockDir, alias, "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("alias lock error = %v, want ErrBusy", err)
	} else if err.Error() != ErrBusy.Error()+": "+alias {
		t.Fatalf("existing working-copy busy diagnostic changed: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(lockDir, alias, "second")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	workingCopy := filepath.Join(root, "wc")
	if err := mkdirAll(workingCopy); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(filepath.Join(root, "locks"), workingCopy, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("重复释放锁失败: %v", err)
	}
}
