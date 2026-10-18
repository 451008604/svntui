package svn

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestClientAgainstLocalRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}

	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	workingCopy := filepath.Join(root, "working-copy")
	runTestCommand(t, root, "svnadmin", "create", repository)
	repositoryURL := fileURL(repository)
	runTestCommand(t, root, "svn", "checkout", repositoryURL, workingCopy)

	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, workingCopy, "svn", "add", "hello.txt")
	runTestCommand(t, workingCopy, "svn", "commit", "-m", "initial")

	client := NewClient()
	info, err := client.Info(t.Context(), workingCopy)
	if err != nil {
		t.Fatal(err)
	}
	if info.UUID == "" || info.URL != repositoryURL {
		t.Fatalf("unexpected info: %+v", info)
	}

	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("changed\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, err := client.Status(t.Context(), workingCopy, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Item != "modified" {
		t.Fatalf("unexpected status: %+v", statuses)
	}

	diff, err := client.Diff(t.Context(), workingCopy, []string{"hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("expected a non-empty diff")
	}

	logs, err := client.Log(t.Context(), workingCopy, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Message != "initial" {
		t.Fatalf("unexpected logs: %+v", logs)
	}

	revisionDiff, err := client.RevisionDiff(t.Context(), workingCopy, "", logs[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(revisionDiff, "hello.txt") || !strings.Contains(revisionDiff, "+hello") {
		t.Fatalf("unexpected revision diff: %s", revisionDiff)
	}
	fileRevisionDiff, err := client.RevisionDiff(t.Context(), workingCopy, "hello.txt", logs[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fileRevisionDiff, "+hello") {
		t.Fatalf("unexpected file revision diff: %s", fileRevisionDiff)
	}

	fileLogs, err := client.Log(t.Context(), filepath.Join(workingCopy, "hello.txt"), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(fileLogs) != 1 || fileLogs[0].Revision != logs[0].Revision {
		t.Fatalf("unexpected file logs: %+v", fileLogs)
	}
}

func TestStatusLoadsTextConflictArtifactsFromInfo(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	runTestCommand(t, root, "svnadmin", "create", repository)
	repositoryURL := fileURL(repository)
	runTestCommand(t, root, "svn", "checkout", repositoryURL, first)
	if err := os.WriteFile(filepath.Join(first, "conflict.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, first, "svn", "add", "conflict.txt")
	runTestCommand(t, first, "svn", "commit", "-m", "base")
	runTestCommand(t, root, "svn", "checkout", repositoryURL, second)
	if err := os.WriteFile(filepath.Join(first, "conflict.txt"), []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, first, "svn", "commit", "-m", "theirs")
	if err := os.WriteFile(filepath.Join(second, "conflict.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, second, "svn", "update")

	entries, err := NewClient().Status(t.Context(), second, false, false)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(entries, func(entry StatusEntry) bool { return entry.IsConflict() })
	if index < 0 {
		t.Fatalf("no conflict in status: %+v", entries)
	}
	conflict := entries[index]
	for name, path := range map[string]string{"BASE": conflict.ConflictOld, "MINE": conflict.ConflictNew, "THEIRS": conflict.ConflictWorking} {
		if path == "" {
			t.Errorf("%s artifact is empty: %+v", name, conflict)
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s artifact %q: %v", name, path, err)
		}
	}
}

func TestRepositoryHistoryOperationsAgainstLocalRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}

	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	workingCopy := filepath.Join(root, "working-copy")
	secondWorkingCopy := filepath.Join(root, "second-working-copy")
	exportDirectory := filepath.Join(root, "export")
	runTestCommand(t, root, "svnadmin", "create", repository)
	repositoryURL := fileURL(repository)
	trunkURL := repositoryURL + "/trunk"
	client := NewClient()

	if _, err := client.RemoteMkdir(t.Context(), trunkURL, "create trunk"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Checkout(t.Context(), trunkURL, workingCopy, "HEAD", "infinity"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, workingCopy, "svn", "add", "hello.txt")
	runTestCommand(t, workingCopy, "svn", "commit", "-m", "add hello")
	branchURL := repositoryURL + "/branch"
	if _, err := client.RemoteCopy(t.Context(), trunkURL, branchURL, "HEAD", "create branch"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("changed\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, workingCopy, "svn", "commit", "-m", "change trunk")
	comparison, err := client.Compare(t.Context(), branchURL, trunkURL, true)
	if err != nil || !strings.Contains(comparison.Stdout, "hello.txt") {
		t.Fatalf("unexpected comparison: %+v, %v", comparison, err)
	}

	entries, err := client.List(t.Context(), trunkURL, "HEAD", "immediates")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" || entries[0].Kind != "file" || entries[0].Revision == 0 {
		t.Fatalf("unexpected list: %+v", entries)
	}

	lines, err := client.Blame(t.Context(), trunkURL+"/hello.txt", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Line != 1 || lines[0].Revision == 0 || lines[1].Line != 2 {
		t.Fatalf("unexpected blame: %+v", lines)
	}

	if _, err := client.Checkout(t.Context(), trunkURL, secondWorkingCopy, "HEAD", "files"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secondWorkingCopy, "hello.txt")); err != nil {
		t.Fatalf("checkout did not create file: %v", err)
	}
	if _, err := client.Export(t.Context(), trunkURL, exportDirectory, "HEAD", "infinity"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exportDirectory, "hello.txt")); err != nil {
		t.Fatalf("export did not create file: %v", err)
	}
}

func runTestCommand(t *testing.T, dir, binary string, args ...string) {
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

func fileURL(path string) string {
	if runtime.GOOS == "windows" {
		return "file:///" + filepath.ToSlash(path)
	}
	return "file://" + filepath.ToSlash(path)
}
