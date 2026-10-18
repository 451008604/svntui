package automation

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"svntui/internal/appdirs"
)

func TestStoreTaskCRUD(t *testing.T) {
	store := testStore(t)
	workingCopy := t.TempDir()
	task, err := store.AddTask(Task{
		Name:           "main",
		Enabled:        true,
		WorkingCopy:    workingCopy,
		RepositoryUUID: "uuid",
		RootURL:        "https://svn.example.test/repo",
		BranchURL:      "https://svn.example.test/repo/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Interval.Duration() != defaultInterval || task.Status != StatusScheduled {
		t.Fatalf("unexpected normalized task: %+v", task)
	}
	if _, err := store.AddTask(task); err == nil {
		t.Fatal("duplicate task was accepted")
	}

	task.Name = "renamed"
	if _, err := store.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "renamed" {
		t.Fatalf("task name = %q, want renamed", found.Name)
	}
	if err := store.PauseTask(task.ID, "maintenance"); err != nil {
		t.Fatal(err)
	}
	found, _ = store.FindTask(task.ID)
	if found.Status != StatusPaused || found.PauseReason != "maintenance" {
		t.Fatalf("task was not paused: %+v", found)
	}
	next := time.Now().Add(time.Hour).UTC()
	if err := store.ResumeTask(task.ID, next); err != nil {
		t.Fatal(err)
	}
	found, _ = store.FindTask(task.ID)
	if found.Status != StatusScheduled || !found.NextRunAt.Equal(next) {
		t.Fatalf("task was not resumed: %+v", found)
	}
	if err := store.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindTask(task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("FindTask error = %v, want ErrTaskNotFound", err)
	}
}

func testStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	return NewStore(appdirs.Dirs{
		Config: filepath.Join(root, "config"),
		State:  filepath.Join(root, "state"),
		Cache:  filepath.Join(root, "cache"),
	})
}
