package safepoint

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestCopiedDirectoryPreservesEveryVersionedNode(t *testing.T) {
	wc := newWorkingCopy(t)
	writeFile(t, filepath.Join(wc, "original", "child.txt"), []byte("copied child\n"))
	writeFile(t, filepath.Join(wc, "original", "binary.dat"), []byte{0, 8, 9, 10})
	writeFile(t, filepath.Join(wc, "original", "changed.txt"), []byte("before\n"))
	writeFile(t, filepath.Join(wc, "original", "removed.txt"), []byte("removed\n"))
	if err := os.Mkdir(filepath.Join(wc, "original", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, wc, "svn", "add", "original")
	run(t, wc, "svn", "propset", "test:directory", "directory value", "original")
	run(t, wc, "svn", "propset", "test:file", "file value", "original/child.txt")
	propertyFile := filepath.Join(t.TempDir(), "binary-property")
	writeFile(t, propertyFile, []byte{0, 1, 255})
	run(t, wc, "svn", "propset", "--file", propertyFile, "test:binary", "original/binary.dat")
	run(t, wc, "svn", "commit", "-m", "source")
	run(t, wc, "svn", "copy", "original", "copied")
	writeFile(t, filepath.Join(wc, "copied", "changed.txt"), []byte("after\n"))
	run(t, wc, "svn", "delete", "copied/removed.txt")
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	entries := entriesByPath(manifest.Entries)
	for _, name := range []string{"copied/child.txt", "copied/binary.dat", "copied/changed.txt"} {
		if entries[name].Payload == "" || entries[name].Status != "added" {
			t.Fatalf("copied child not fully captured: %+v", entries[name])
		}
	}
	run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
	result, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(wc, "copied", "child.txt"), []byte("copied child\n"))
	assertFile(t, filepath.Join(wc, "copied", "binary.dat"), []byte{0, 8, 9, 10})
	assertFile(t, filepath.Join(wc, "copied", "changed.txt"), []byte("after\n"))
	if _, err := os.Stat(filepath.Join(wc, "copied", "removed.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted copy descendant restored: %v", err)
	}
	if info, err := os.Stat(filepath.Join(wc, "copied", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty copied directory: %v %v", info, err)
	}
	if got := strings.TrimSpace(run(t, wc, "svn", "propget", "test:directory", "copied")); got != "directory value" {
		t.Fatalf("directory property = %q", got)
	}
	if got := strings.TrimSpace(run(t, wc, "svn", "propget", "test:file", "copied/child.txt")); got != "file value" {
		t.Fatalf("file property = %q", got)
	}
	properties, err := manager.client.Properties(t.Context(), wc, "copied/binary.dat")
	if err != nil || properties["test:binary"] != string([]byte{0, 1, 255}) {
		t.Fatalf("binary property = %q, %v", properties["test:binary"], err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("missing copy ancestry warning")
	}
}

func TestRejectBinaryConflictBeforeCreatingSnapshot(t *testing.T) {
	wc := newWorkingCopy(t)
	root := filepath.Dir(wc)
	other := filepath.Join(root, "other")
	url := strings.TrimSpace(run(t, wc, "svn", "info", "--show-item", "url"))
	run(t, root, "svn", "checkout", url, other)
	writeFile(t, filepath.Join(other, "binary.dat"), []byte{0, 9, 9, 9})
	run(t, other, "svn", "commit", "-m", "remote binary")
	local := []byte{0, 7, 7, 7}
	writeFile(t, filepath.Join(wc, "binary.dat"), local)
	run(t, wc, "svn", "update")
	manager := testManager(t)
	if _, err := manager.Create(t.Context(), wc, CreateOptions{}); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("incomplete conflict snapshot accepted: %v", err)
	}
	assertFile(t, filepath.Join(wc, "binary.dat"), local)
	manifests, err := manager.List()
	if err != nil || len(manifests) != 0 {
		t.Fatalf("incomplete snapshot persisted: %+v %v", manifests, err)
	}
}

func TestRejectConflictSnapshotBeforeChangingDirtyTarget(t *testing.T) {
	wc := newWorkingCopy(t)
	manager := testManager(t)
	writeFile(t, filepath.Join(wc, "binary.dat"), []byte{0, 7, 7, 7})
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Entries[0].Status = "conflicted"
	writeManifest(t, manager, manifest)
	current := []byte{0, 4, 4, 4}
	writeFile(t, filepath.Join(wc, "binary.dat"), current)
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{CreateBackup: true}); err == nil {
		t.Fatal("restore accepted an unsupported conflict state")
	}
	assertFile(t, filepath.Join(wc, "binary.dat"), current)
}

func TestVersionedDirectorySymlinkRestoresLiteralNode(t *testing.T) {
	wc := newWorkingCopy(t)
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(wc, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(wc, "link")
	if err := os.Symlink("first", link); err != nil {
		// 仅在 Windows 缺少符号链接权限（ERROR_PRIVILEGE_NOT_HELD）时跳过。
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("creating symbolic links requires additional privileges: %v", err)
		}
		t.Fatal(err)
	}
	run(t, wc, "svn", "add", "first", "second", "link")
	run(t, wc, "svn", "commit", "-m", "symlink")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second", link); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if entry := entriesByPath(manifest.Entries)["link"]; !strings.HasSuffix(entry.BaseURL, "/link") {
		t.Fatalf("symlink identity followed directory target: %+v", entry)
	}
	run(t, wc, "svn", "revert", "link")
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(link)
	if err != nil || got != "second" {
		t.Fatalf("link=%q err=%v", got, err)
	}
}

func TestReplacementDropsPropertiesSpecificToOldKind(t *testing.T) {
	for _, oldDirectory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file-to-directory", true: "directory-to-file"}[oldDirectory], func(t *testing.T) {
			wc := newWorkingCopy(t)
			path, property, value := "node", "svn:mime-type", "text/plain"
			if oldDirectory {
				path, property, value = "node/child", "svn:ignore", "*.tmp"
			}
			writeFile(t, filepath.Join(wc, path), []byte("old\n"))
			run(t, wc, "svn", "add", "node")
			run(t, wc, "svn", "propset", property, value, "node")
			run(t, wc, "svn", "commit", "-m", "old kind")
			run(t, wc, "svn", "delete", "node")
			if oldDirectory {
				writeFile(t, filepath.Join(wc, "node"), []byte("new\n"))
			} else if err := os.Mkdir(filepath.Join(wc, "node"), 0o700); err != nil {
				t.Fatal(err)
			}
			run(t, wc, "svn", "add", "node")
			manager := testManager(t)
			manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
			if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
				t.Fatal(err)
			}
			properties, err := manager.client.Properties(t.Context(), wc, "node")
			if err != nil || len(properties) != 0 {
				t.Fatalf("old type property leaked: %+v %v", properties, err)
			}
		})
	}
}
func TestRestorePatchNotificationNames(t *testing.T) {
	wc := newWorkingCopy(t)
	names := []string{"conflict.txt", "skipped.txt", "rejected hunk.txt"}
	for _, name := range names {
		writeFile(t, filepath.Join(wc, name), []byte("base\n"))
		run(t, wc, "svn", "add", name)
	}
	run(t, wc, "svn", "commit", "-m", "notification names")
	for _, name := range names {
		writeFile(t, filepath.Join(wc, name), []byte("modified\n"))
	}
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, wc, "svn", "revert", "--depth", "infinity", ".")
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		assertFile(t, filepath.Join(wc, name), []byte("modified\n"))
	}
}
