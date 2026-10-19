package wclock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireKeyScopesIdentityToNamespace(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "locks")
	first, err := AcquireKey(directory, "opaque-project-id", "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if other, err := AcquireKey(directory, "opaque-project-id", "second"); !errors.Is(err, ErrBusy) {
		_ = other.Release()
		t.Fatalf("same key did not conflict: %v", err)
	}
	for _, pair := range [][2]string{{directory, "another-project-id"}, {filepath.Join(root, "other-locks"), "opaque-project-id"}} {
		other, err := AcquireKey(pair[0], pair[1], "independent")
		if err != nil {
			t.Fatalf("independent key/namespace conflicted: %v", err)
		}
		_ = other.Release()
	}
	if _, err := AcquireKey(directory, "", "invalid"); err == nil {
		t.Fatal("empty opaque key accepted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected namespace entries: %v / %v", entries, err)
	}
	// Windows 的排他字节锁也拒绝第二个读取句柄，释放后检查同一文件的元数据。
	metadataPath := first.file.Name()
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read released lock metadata: %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\n")
	if len(fields) != 4 || fields[2] != "first" || fields[3] != "opaque-project-id" {
		t.Fatalf("missing exact opaque identity metadata: %q", metadata)
	}
	again, err := AcquireKey(directory, "opaque-project-id", "after release")
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireKeyUsesPhysicalNamespaceForAliases(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "State", "locks")
	if err := os.MkdirAll(original, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireKey(original, "project", "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	// 相同键经过不同工作目录解析到同一锁目录，仍只能取得一次。
	t.Chdir(root)
	if other, err := AcquireKey(filepath.Join("State", "locks"), "project", "relative"); !errors.Is(err, ErrBusy) {
		_ = other.Release()
		t.Fatalf("relative namespace bypassed lock: %v", err)
	}
	t.Chdir(filepath.Join(root, "State"))
	if other, err := AcquireKey("locks", "project", "different cwd"); !errors.Is(err, ErrBusy) {
		_ = other.Release()
		t.Fatalf("different cwd bypassed lock: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(original, alias); err == nil {
		if other, err := AcquireKey(alias, "project", "symlink"); !errors.Is(err, ErrBusy) {
			_ = other.Release()
			t.Fatalf("symlink namespace bypassed lock: %v", err)
		}
	}
	// 大小写敏感磁盘上的不同目录保持独立；不使用全局小写路径键。
	caseAlias := filepath.Join(root, "state", "locks")
	if err := os.MkdirAll(caseAlias, 0o700); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(caseAlias)
	if err != nil {
		t.Fatal(err)
	}
	other, err := AcquireKey(caseAlias, "project", "case")
	if os.SameFile(originalInfo, aliasInfo) {
		if !errors.Is(err, ErrBusy) {
			_ = other.Release()
			t.Fatalf("case alias bypassed physical namespace: %v", err)
		}
	} else {
		if err != nil {
			t.Fatalf("distinct case-sensitive namespace conflicted: %v", err)
		}
		_ = other.Release()
	}
}
