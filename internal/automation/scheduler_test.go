package automation

import (
	"testing"
	"time"
)

func TestSchedulerRecordsOneSkipOutsideActiveWindow(t *testing.T) {
	store := testStore(t)
	task := addRunnerTask(t, store)
	task.TimeZone = "UTC"
	task.Schedule = Schedule{
		Weekdays: []time.Weekday{time.Monday},
		Windows:  []ActiveWindow{{Start: "09:00", End: "10:00"}},
	}
	if _, err := store.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 23, 30, 0, 0, time.UTC)
	runner := NewRunner(store, &fakeSVN{})
	runner.Now = func() time.Time { return now }
	runner.Jitter = func(time.Duration) time.Duration { return 0 }
	scheduler := NewScheduler(store, runner)
	if err := scheduler.RunDue(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunDue(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != StatusSkipped {
		t.Fatalf("unexpected scheduler records: %+v", runs)
	}
	found, _ := store.FindTask(task.ID)
	wantNext := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	if !found.NextRunAt.Equal(wantNext) {
		t.Fatalf("next run = %v, want %v", found.NextRunAt, wantNext)
	}
}
