package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	"svntui/internal/svn"
)

type fakeSVN struct {
	info          svn.Info
	infoErr       error
	status        []svn.StatusEntry
	postStatus    []svn.StatusEntry
	statusErr     error
	update        svn.Result
	updateErr     error
	updateStarted chan struct{}
	updateRelease chan struct{}
	statusCalls   int
	updateCalls   int
}

func (f *fakeSVN) Info(context.Context, string) (svn.Info, error) {
	return f.info, f.infoErr
}

func (f *fakeSVN) Status(context.Context, string, bool, bool) ([]svn.StatusEntry, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if f.statusCalls > 1 {
		return f.postStatus, nil
	}
	return f.status, nil
}

func (f *fakeSVN) Update(context.Context, string, bool) (svn.Result, error) {
	f.updateCalls++
	if f.updateStarted != nil {
		close(f.updateStarted)
		<-f.updateRelease
	}
	return f.update, f.updateErr
}

func TestRunnerUsesUpdateChangesForMixedRevisionWorkingCopy(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{
		info:   taskInfo(task, 0),
		update: svn.Result{Stdout: "U    src/main.go\nUpdated to revision 7.\n"},
	}
	runner := NewRunner(store, client)
	runner.Now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != OutcomeChanged || len(record.Changes) != 1 {
		t.Fatalf("unexpected update outcome: %+v", record)
	}
	if record.AfterRevision != 7 {
		t.Fatalf("after revision = %d, want 7", record.AfterRevision)
	}
	found, _ := store.FindTask(task.ID)
	if found.LastRevision != 7 || found.Status != StatusSucceeded {
		t.Fatalf("unexpected persisted task: %+v", found)
	}
}

func TestRunnerNoChangeDoesNotDependOnRootRevision(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{
		info:   taskInfo(task, 0),
		update: svn.Result{Stdout: "At revision 7.\n"},
	}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != OutcomeNoChange || record.AfterRevision != 7 {
		t.Fatalf("unexpected no-change record: %+v", record)
	}
}

func TestRunnerSkipsDirtyWorkingCopy(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{
		info:   taskInfo(task, 1),
		status: []svn.StatusEntry{{Path: "local.go", Item: "modified"}},
	}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	record, err := runner.RunTask(t.Context(), task.ID, TriggerScheduled)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusSkipped || client.updateCalls != 0 {
		t.Fatalf("dirty update was not skipped: record=%+v calls=%d", record, client.updateCalls)
	}
}

func TestRunnerKeepsDisabledAndPausedStates(t *testing.T) {
	for _, status := range []TaskStatus{StatusDisabled, StatusPaused} {
		t.Run(string(status), func(t *testing.T) {
			store := testStore(t)
			task := addRunnerTask(t, store)
			if status == StatusDisabled {
				task.Enabled = false
			}
			task.Status = status
			if status == StatusPaused {
				task.PauseReason = "maintenance"
			}
			if _, err := store.UpdateTask(task); err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(store, &fakeSVN{})
			record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
			if err != nil || record.Status != StatusSkipped {
				t.Fatalf("inactive run result: record=%+v error=%v", record, err)
			}
			found, _ := store.FindTask(task.ID)
			if found.Status != status || !found.NextRunAt.IsZero() {
				t.Fatalf("inactive state changed: %+v", found)
			}
		})
	}
}

func TestRunnerDoesNotOverwritePauseDuringUpdate(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{
		info: taskInfo(task, 1), update: svn.Result{Stdout: "Updated to revision 2.\n"},
		updateStarted: make(chan struct{}), updateRelease: make(chan struct{}),
	}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
		done <- err
	}()
	<-client.updateStarted
	if err := runner.PauseTask(t.Context(), task.ID, "maintenance"); err != nil {
		t.Fatal(err)
	}
	close(client.updateRelease)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	found, err := store.FindTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != StatusPaused || found.PauseReason != "maintenance" || !found.NextRunAt.IsZero() {
		t.Fatalf("pause was overwritten: %+v", found)
	}
}

func TestRunnerHonorsPauseBeforeMarkingRunning(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	runner := NewRunner(store, &fakeSVN{info: taskInfo(task, 1)})
	reached := make(chan struct{})
	release := make(chan struct{})
	runner.beforeMarkRunning = func() {
		close(reached)
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
		done <- err
	}()
	<-reached
	if err := runner.PauseTask(t.Context(), task.ID, "maintenance"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	found, err := store.FindTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Status != StatusPaused || found.PauseReason != "maintenance" {
		t.Fatalf("pre-start pause was overwritten: %+v", found)
	}
}

func TestRunnerHonorsDeleteBeforeMarkingRunning(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	runner := NewRunner(store, &fakeSVN{info: taskInfo(task, 1)})
	reached := make(chan struct{})
	release := make(chan struct{})
	runner.beforeMarkRunning = func() {
		close(reached)
		<-release
	}
	type outcome struct {
		record RunRecord
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
		done <- outcome{record: record, err: err}
	}()
	<-reached
	if err := store.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.err != nil || result.record.ID == "" || result.record.Status != StatusSkipped {
		t.Fatalf("pre-start delete result = %+v", result)
	}
	if _, err := store.FindTask(task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("deleted task was recreated: %v", err)
	}
}

func TestRunnerDeleteDuringUpdateDoesNotFailScheduler(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{
		info: taskInfo(task, 1), update: svn.Result{Stdout: "At revision 1.\n"},
		updateStarted: make(chan struct{}), updateRelease: make(chan struct{}),
	}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	type outcome struct {
		record RunRecord
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
		done <- outcome{record: record, err: err}
	}()
	<-client.updateStarted
	if err := store.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	close(client.updateRelease)
	result := <-done
	if result.err != nil || result.record.ID == "" {
		t.Fatalf("deleted in-flight task result = %+v", result)
	}
	if _, err := store.FindTask(task.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("deleted task was recreated: %v", err)
	}
}

func TestRunnerPausesOnIdentityMismatch(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	client := &fakeSVN{info: taskInfo(task, 1)}
	client.info.URL = "https://svn.example.test/repo/other"
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	record, err := runner.RunTask(t.Context(), task.ID, TriggerScheduled)
	if err == nil || record.Status != StatusPaused {
		t.Fatalf("identity mismatch result: record=%+v error=%v", record, err)
	}
	found, _ := store.FindTask(task.ID)
	if found.Status != StatusPaused || !found.NextRunAt.IsZero() {
		t.Fatalf("task was not persistently paused: %+v", found)
	}
}

func TestRunnerPausesAfterThreeNetworkFailures(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	networkErr := &svn.CommandError{Kind: svn.ErrorNetwork, Err: errors.New("network unavailable")}
	client := &fakeSVN{info: taskInfo(task, 1), updateErr: networkErr}
	runner := NewRunner(store, client)
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	for attempt := 1; attempt <= 3; attempt++ {
		client.statusCalls = 0
		record, err := runner.RunTask(t.Context(), task.ID, TriggerManual)
		if !errors.Is(err, networkErr) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
		want := StatusFailed
		if attempt == 3 {
			want = StatusPaused
		}
		if record.Status != want {
			t.Fatalf("attempt %d status = %s, want %s", attempt, record.Status, want)
		}
	}
	found, _ := store.FindTask(task.ID)
	if found.Status != StatusPaused || found.ConsecutiveFailures != 3 {
		t.Fatalf("unexpected failure state: %+v", found)
	}
}

func addRunnerTask(t *testing.T, store Store) Task {
	t.Helper()
	task, err := store.AddTask(Task{
		Name:           "main",
		Enabled:        true,
		WorkingCopy:    t.TempDir(),
		RepositoryUUID: "uuid",
		RootURL:        "https://svn.example.test/repo",
		BranchURL:      "https://svn.example.test/repo/main",
		Interval:       Duration(15 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func taskInfo(task Task, revision int64) svn.Info {
	return svn.Info{
		Revision: revision,
		UUID:     task.RepositoryUUID,
		RootURL:  task.RootURL,
		URL:      task.BranchURL,
	}
}
