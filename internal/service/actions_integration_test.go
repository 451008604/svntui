package service

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"svntui/internal/svn"
	"svntui/internal/tui"
)

func TestCommitDirectoryDoesNotIncludeUnselectedChildren(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	workingCopy := filepath.Join(root, "wc")
	runServiceCommand(t, root, "svnadmin", "create", repository)
	url := serviceFileURL(repository)
	runServiceCommand(t, root, "svn", "checkout", url, workingCopy)
	if err := os.Mkdir(filepath.Join(workingCopy, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workingCopy, "dir", name), []byte("base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runServiceCommand(t, workingCopy, "svn", "add", "dir")
	runServiceCommand(t, workingCopy, "svn", "commit", "-m", "base")
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workingCopy, "dir", name), []byte("local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runServiceCommand(t, workingCopy, "svn", "propset", "test:selected", "yes", "dir")
	actions := Actions{Client: svn.NewClient(), LockDir: filepath.Join(root, "locks")}
	_, err := actions.Execute(t.Context(), tui.ActionRequest{
		Action: tui.ActionCommit, WorkingCopy: workingCopy, Targets: []string{"dir"}, Arguments: []string{"directory property only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := actions.Client.Status(t.Context(), workingCopy, false, false)
	if err != nil {
		t.Fatal(err)
	}
	modified := map[string]bool{}
	for _, entry := range entries {
		if entry.Item == "modified" {
			modified[filepath.ToSlash(entry.Path)] = true
		}
	}
	if !modified["dir/a.txt"] || !modified["dir/b.txt"] {
		t.Fatalf("unselected children were committed: %+v", entries)
	}
}

func TestWorkspaceDiffCombinesVersionedAndUnversionedContent(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	workingCopy := filepath.Join(root, "wc")
	runServiceCommand(t, root, "svnadmin", "create", repository)
	runServiceCommand(t, root, "svn", "checkout", serviceFileURL(repository), workingCopy)
	if err := os.WriteFile(filepath.Join(workingCopy, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runServiceCommand(t, workingCopy, "svn", "add", "tracked.txt")
	runServiceCommand(t, workingCopy, "svn", "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(workingCopy, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingCopy, "new.txt"), []byte("第一行\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}

	actions := Actions{Client: svn.NewClient()}
	diff, err := actions.WorkspaceDiff(t.Context(), workingCopy, []string{"tracked.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"-base", "+changed", "状态：unversioned", "+第一行", "+second", "No newline at end of file"} {
		if !strings.Contains(diff, expected) {
			t.Fatalf("工作区差异缺少 %q：\n%s", expected, diff)
		}
	}
}

func TestUnversionedBinaryAndDirectoryDiffReturnGuidance(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "asset.bin")
	directory := filepath.Join(root, "folder")
	if err := os.WriteFile(binary, []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPreview, err := previewUnversionedFile("asset.bin", binary, "unversioned")
	if err != nil || !strings.Contains(binaryPreview, "二进制文件") {
		t.Fatalf("二进制预览 = %q, %v", binaryPreview, err)
	}
	directoryPreview, err := previewUnversionedFile("folder", directory, "unversioned")
	if err != nil || !strings.Contains(directoryPreview, "选择具体文件") {
		t.Fatalf("目录预览 = %q, %v", directoryPreview, err)
	}
}

func TestContainsConflictMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflict.txt")
	if err := os.WriteFile(path, []byte("<<<<<<< mine\na\n=======\nb\n>>>>>>> theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marked, err := containsConflictMarkers(path)
	if err != nil || !marked {
		t.Fatalf("containsConflictMarkers() = %v, %v", marked, err)
	}
}

func TestContainsConflictMarkersBeyondEightMiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-conflict.txt")
	data := append(bytes.Repeat([]byte("a"), 9<<20), []byte("\n<<<<<<< mine\na\n=======\nb\n>>>>>>> theirs\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	marked, err := containsConflictMarkers(path)
	if err != nil || !marked {
		t.Fatalf("containsConflictMarkers() = %v, %v", marked, err)
	}
}

func runServiceCommand(t *testing.T, dir, binary string, args ...string) {
	t.Helper()
	if binary == "svn" {
		args = append([]string{"--non-interactive"}, args...)
	}
	command := exec.CommandContext(t.Context(), binary, args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, output)
	}
}

func serviceFileURL(path string) string {
	if runtime.GOOS == "windows" {
		return "file:///" + filepath.ToSlash(path)
	}
	return "file://" + filepath.ToSlash(path)
}
