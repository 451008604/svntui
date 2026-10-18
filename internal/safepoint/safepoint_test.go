package safepoint

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/storage"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

func TestCreateAndRestoreRoundTrip(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	writeFile(t, filepath.Join(workingCopy, "text.txt"), []byte("base\nchanged\n"))
	writeFile(t, filepath.Join(workingCopy, "binary.dat"), []byte{0, 9, 8, 7})
	writeFile(t, filepath.Join(workingCopy, "new.txt"), []byte("new\n"))
	run(t, workingCopy, "svn", "add", "new.txt")
	if err := os.Mkdir(filepath.Join(workingCopy, "empty-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, workingCopy, "svn", "add", "empty-dir")
	run(t, workingCopy, "svn", "move", "old-name.txt", "new-name.txt")
	run(t, workingCopy, "svn", "propset", "test:safepoint", "root-value", ".")
	writeFile(t, filepath.Join(workingCopy, "notes.txt"), []byte("unversioned\n"))
	writeFile(t, filepath.Join(workingCopy, "extras", "nested", "data.txt"), []byte("directory payload\n"))

	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), workingCopy, CreateOptions{
		Reason:             "round trip",
		IncludeUnversioned: []string{"notes.txt", "extras"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if manifest.Patch == "" {
		t.Fatal("Create() did not record an svn patch")
	}
	entries := entriesByPath(manifest.Entries)
	for _, path := range []string{".", "text.txt", "binary.dat", "empty-dir", "new.txt", "old-name.txt", "new-name.txt", "notes.txt", "extras"} {
		if _, exists := entries[path]; !exists {
			t.Errorf("Create() did not record %s", path)
		}
	}
	if !entries["binary.dat"].Binary || entries["binary.dat"].Payload == "" {
		t.Errorf("binary entry = %+v, want binary payload", entries["binary.dat"])
	}
	if !entries["notes.txt"].Unversioned {
		t.Errorf("notes entry = %+v, want unversioned", entries["notes.txt"])
	}
	if entries["new-name.txt"].MovedFrom != "old-name.txt" {
		t.Errorf("move relation = %q, want old-name.txt", entries["new-name.txt"].MovedFrom)
	}
	if entries["."].PropertyState != "modified" {
		t.Errorf("working-copy root entry = %+v, want modified property", entries["."])
	}

	run(t, workingCopy, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
	for _, path := range []string{"new.txt", "notes.txt", "empty-dir", "extras"} {
		if err := os.RemoveAll(filepath.Join(workingCopy, path)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Restore(t.Context(), manifest.ID, workingCopy, RestoreOptions{})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !result.AppliedPatch {
		t.Error("Restore() did not apply the stored patch")
	}
	assertFile(t, filepath.Join(workingCopy, "text.txt"), []byte("base\nchanged\n"))
	assertFile(t, filepath.Join(workingCopy, "binary.dat"), []byte{0, 9, 8, 7})
	assertFile(t, filepath.Join(workingCopy, "new.txt"), []byte("new\n"))
	assertFile(t, filepath.Join(workingCopy, "new-name.txt"), []byte("move me\n"))
	assertFile(t, filepath.Join(workingCopy, "notes.txt"), []byte("unversioned\n"))
	assertFile(t, filepath.Join(workingCopy, "extras", "nested", "data.txt"), []byte("directory payload\n"))
	if info, err := os.Stat(filepath.Join(workingCopy, "empty-dir")); err != nil || !info.IsDir() {
		t.Errorf("empty added directory was not restored: info=%v err=%v", info, err)
	}
	if property := strings.TrimSpace(run(t, workingCopy, "svn", "propget", "test:safepoint", ".")); property != "root-value" {
		t.Errorf("restored root property = %q, want root-value", property)
	}
	if _, err := os.Stat(filepath.Join(workingCopy, "old-name.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old-name.txt still exists after restore, error = %v", err)
	}
	status := run(t, workingCopy, "svn", "status")
	for _, marker := range []string{"M       text.txt", "M       binary.dat", "A       empty-dir", "A       new-name.txt", "D       old-name.txt", "?       notes.txt", "?       extras"} {
		if !strings.Contains(status, marker) {
			t.Errorf("restored status does not contain %q:\n%s", marker, status)
		}
	}
	if len(result.Warnings) == 0 {
		t.Error("Restore() did not warn that svn patch cannot recreate move history")
	}
}

func TestRestoreDirtyWorkingCopyRequiresBackup(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	writeFile(t, filepath.Join(workingCopy, "text.txt"), []byte("snapshot\n"))
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), workingCopy, CreateOptions{Reason: "target"})
	if err != nil {
		t.Fatal(err)
	}
	run(t, workingCopy, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
	writeFile(t, filepath.Join(workingCopy, "text.txt"), []byte("current dirty state\n"))
	manager.policy = RetentionPolicy{MaxAge: time.Nanosecond, MaxBytes: 1 << 30}

	if _, err := manager.Restore(t.Context(), manifest.ID, workingCopy, RestoreOptions{}); !errors.Is(err, ErrDirtyWorkingCopy) {
		t.Fatalf("Restore() error = %v, want ErrDirtyWorkingCopy", err)
	}
	result, err := manager.Restore(t.Context(), manifest.ID, workingCopy, RestoreOptions{CreateBackup: true})
	if err != nil {
		t.Fatalf("Restore(CreateBackup) error = %v", err)
	}
	if result.BackupID == "" || result.BackupID == manifest.ID {
		t.Errorf("backup id = %q, want a new SafePoint", result.BackupID)
	}
	assertFile(t, filepath.Join(workingCopy, "text.txt"), []byte("snapshot\n"))
	backup, err := manager.Inspect(result.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if backup.Reason == "" {
		t.Error("pre-restore backup has no reason")
	}
	if _, err := manager.Inspect(manifest.ID); err != nil {
		t.Errorf("source SafePoint was cleaned while restore was using it: %v", err)
	}
}

func TestSafePointRestoresSelectedSymbolicLink(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	link := filepath.Join(workingCopy, "local-link")
	if err := os.Symlink("text.txt", link); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), workingCopy, CreateOptions{IncludeUnversioned: []string{"local-link"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Restore(t.Context(), manifest.ID, workingCopy, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(link)
	if err != nil || target != "text.txt" {
		t.Fatalf("restored link = %q, %v", target, err)
	}
}

func TestSafePointRejectsSelectedPathThroughSymbolicLink(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("outside"))
	if err := os.Symlink(outside, filepath.Join(workingCopy, "outside")); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	manager := testManager(t)
	if _, err := manager.Create(t.Context(), workingCopy, CreateOptions{IncludeUnversioned: []string{"outside/secret.txt"}}); err == nil {
		t.Fatal("Create() accepted a path through an unversioned symbolic link")
	}
	assertFile(t, filepath.Join(outside, "secret.txt"), []byte("outside"))
}

func TestSafePointRejectsMixedRevisionEntryDrift(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	writeFile(t, filepath.Join(workingCopy, "text.txt"), []byte("snapshot\n"))
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), workingCopy, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, workingCopy, "svn", "revert", "text.txt")
	repositoryURL := strings.TrimSpace(run(t, workingCopy, "svn", "info", "--show-item", "repos-root-url"))
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "svn", "checkout", repositoryURL+"/trunk", other)
	writeFile(t, filepath.Join(other, "text.txt"), []byte("remote\n"))
	run(t, other, "svn", "commit", "-m", "remote", "text.txt")
	run(t, workingCopy, "svn", "update", "text.txt")

	validation, err := manager.Validate(t.Context(), manifest.ID, workingCopy)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Valid || !slices.Contains(validation.Problems, ProblemEntryRevision) || !slices.Contains(validation.EntryPaths, "text.txt") {
		t.Fatalf("mixed revision validation = %+v", validation)
	}
	if _, err := manager.Restore(t.Context(), manifest.ID, workingCopy, RestoreOptions{}); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("Restore() error = %v, want ErrRevisionMismatch", err)
	}
}

func TestSafePointUsesWorkingCopyWriteLock(t *testing.T) {
	workingCopy := newWorkingCopy(t)
	manager := testManager(t)
	lock, err := wclock.Acquire(manager.lockDir, workingCopy, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := manager.Create(t.Context(), workingCopy, CreateOptions{}); !errors.Is(err, wclock.ErrBusy) {
		t.Fatalf("Create() error = %v, want ErrBusy", err)
	}
}

func TestValidateManifestChecksRepositoryAndRevision(t *testing.T) {
	manifest := Manifest{Version: manifestVersion, RepositoryUUID: "uuid-a", RootURL: "https://example.test/repo", URL: "https://example.test/repo/trunk", BaseRevision: 10}
	validation := validateManifest(manifest, svn.Info{UUID: "uuid-b", RootURL: "https://example.test/other", URL: "https://example.test/other/trunk", Revision: 11})
	if validation.Valid || len(validation.Problems) != 4 {
		t.Fatalf("validateManifest() = %+v, want four problems", validation)
	}
	errorValue := &ValidationError{Validation: validation}
	if !errors.Is(errorValue, ErrRepositoryMismatch) || !errors.Is(errorValue, ErrRevisionMismatch) {
		t.Errorf("ValidationError does not expose mismatch sentinels: %v", errorValue)
	}
}

func TestCleanupProtectsLatestPerWorkingCopy(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	manager := testManager(t)
	manager.now = func() time.Time { return now }
	manager.policy = RetentionPolicy{MaxAge: 30 * 24 * time.Hour, MaxBytes: 1 << 20}
	writeManifest(t, manager, Manifest{Version: manifestVersion, ID: "wc-a-old", CreatedAt: now.Add(-90 * 24 * time.Hour), WorkingCopy: "/wc/a"})
	writeManifest(t, manager, Manifest{Version: manifestVersion, ID: "wc-a-new", CreatedAt: now.Add(-60 * 24 * time.Hour), WorkingCopy: "/wc/a"})
	writeManifest(t, manager, Manifest{Version: manifestVersion, ID: "wc-b-only", CreatedAt: now.Add(-90 * 24 * time.Hour), WorkingCopy: "/wc/b"})

	result, err := manager.Cleanup()
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if fmt.Sprint(result.DeletedIDs) != "[wc-a-old]" {
		t.Errorf("deleted ids = %v, want [wc-a-old]", result.DeletedIDs)
	}
	for _, id := range []string{"wc-a-new", "wc-b-only"} {
		if _, err := manager.Inspect(id); err != nil {
			t.Errorf("protected SafePoint %s was deleted: %v", id, err)
		}
	}
}

func TestCleanupReturnsCapacityErrorWhenOnlyProtectedSnapshotsRemain(t *testing.T) {
	manager := testManager(t)
	manager.policy = RetentionPolicy{MaxBytes: 1}
	writeManifest(t, manager, Manifest{Version: manifestVersion, ID: "only", CreatedAt: time.Now(), WorkingCopy: "/wc/a"})

	result, err := manager.Cleanup()
	if !errors.Is(err, ErrCapacity) || !result.CapacityExceeded {
		t.Fatalf("Cleanup() = (%+v, %v), want ErrCapacity", result, err)
	}
	if _, err := manager.Inspect("only"); err != nil {
		t.Errorf("protected SafePoint was deleted: %v", err)
	}
}

func TestDeleteRejectsPathTraversal(t *testing.T) {
	manager := testManager(t)
	outside := filepath.Join(filepath.Dir(manager.root), "outside")
	writeFile(t, outside, []byte("keep"))
	if err := manager.Delete("../outside"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Delete() error = %v, want ErrInvalidID", err)
	}
	assertFile(t, outside, []byte("keep"))
}

func newWorkingCopy(t *testing.T) string {
	t.Helper()
	for _, command := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s is required for integration test", command)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	workingCopy := filepath.Join(root, "wc")
	run(t, root, "svnadmin", "create", repository)
	repositoryURL := testRepositoryURL(repository)
	run(t, root, "svn", "mkdir", repositoryURL+"/trunk", "-m", "initialize")
	run(t, root, "svn", "checkout", repositoryURL+"/trunk", workingCopy)
	writeFile(t, filepath.Join(workingCopy, "text.txt"), []byte("base\n"))
	writeFile(t, filepath.Join(workingCopy, "binary.dat"), []byte{0, 1, 2, 3})
	writeFile(t, filepath.Join(workingCopy, "old-name.txt"), []byte("move me\n"))
	run(t, workingCopy, "svn", "add", "text.txt", "binary.dat", "old-name.txt")
	run(t, workingCopy, "svn", "commit", "-m", "base")
	return workingCopy
}

func testRepositoryURL(path string) string {
	// Windows 盘符前保留 URL 根斜线，并编码空格及非 ASCII 路径。
	path = "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func TestRepositoryURLSupportsDrivePaths(t *testing.T) {
	for _, path := range []string{"/tmp/仓库 test/repo", "C:/Users/仓库 test/repo"} {
		parsed, err := url.Parse(testRepositoryURL(filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != "/"+strings.TrimPrefix(path, "/") {
			t.Fatalf("repository URL for %q = %#v", path, parsed)
		}
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	return New(appdirs.Dirs{State: state}, svn.NewClient())
}

func entriesByPath(entries []Entry) map[string]Entry {
	result := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		result[entry.Path] = entry
	}
	return result
}

func writeManifest(t *testing.T, manager *Manager, manifest Manifest) {
	t.Helper()
	directory := filepath.Join(manager.root, manifest.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveJSON(filepath.Join(directory, manifestFilename), manifest); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

func run(t *testing.T, directory, command string, arguments ...string) string {
	t.Helper()
	if command == "svn" {
		arguments = append([]string{"--non-interactive"}, arguments...)
	}
	cmd := exec.CommandContext(t.Context(), command, arguments...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", command, arguments, err, output)
	}
	return string(output)
}
