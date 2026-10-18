package automation

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/svn"
)

func TestRunnerUpdatesLocalWorkingCopy(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	producer := filepath.Join(root, "producer")
	consumer := filepath.Join(root, "consumer")
	runAutomationCommand(t, root, "svnadmin", "create", repository)
	url := automationFileURL(repository)
	runAutomationCommand(t, root, "svn", "checkout", url, producer)
	if err := os.WriteFile(filepath.Join(producer, "value.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAutomationCommand(t, producer, "svn", "add", "value.txt")
	runAutomationCommand(t, producer, "svn", "commit", "-m", "one")
	runAutomationCommand(t, root, "svn", "checkout", url, consumer)
	if err := os.WriteFile(filepath.Join(producer, "value.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAutomationCommand(t, producer, "svn", "commit", "-m", "two")

	client := svn.NewClient()
	store := NewStore(appdirs.Dirs{State: filepath.Join(root, "state")})
	task, err := NewTask(t.Context(), client, "consumer", consumer)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.AddTask(task)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusSucceeded || record.Outcome != OutcomeChanged || len(record.Changes) == 0 {
		t.Fatalf("update result = %+v", record)
	}
	data, err := os.ReadFile(filepath.Join(consumer, "value.txt"))
	if err != nil || string(data) != "two\n" {
		t.Fatalf("updated file = %q, %v", data, err)
	}
	record, err = runner.RunTask(t.Context(), task.ID, TriggerManual)
	if err != nil || record.Outcome != OutcomeNoChange {
		t.Fatalf("no-change result = %+v, %v", record, err)
	}
}

func runAutomationCommand(t *testing.T, dir, binary string, args ...string) {
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

func automationFileURL(path string) string {
	if runtime.GOOS == "windows" {
		return "file:///" + filepath.ToSlash(path)
	}
	return "file://" + filepath.ToSlash(path)
}
