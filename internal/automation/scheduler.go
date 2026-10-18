package automation

import (
	"context"
	"errors"
	"time"
)

const schedulerMaxPoll = time.Minute

// Scheduler 在进程内调度到期任务；同一 Runner 也可供无界面后台进程使用。
type Scheduler struct {
	Store  Store
	Runner Runner
	Now    func() time.Time
}

func NewScheduler(store Store, runner Runner) Scheduler {
	return Scheduler{Store: store, Runner: runner}
}

func (s Scheduler) Run(ctx context.Context) error {
	for {
		now := s.currentTime()
		if err := s.RunDue(ctx, now); err != nil {
			if !waitSchedulerRetry(ctx) {
				return context.Cause(ctx)
			}
			continue
		}
		delay, err := s.nextDelay(s.currentTime())
		if err != nil {
			if !waitSchedulerRetry(ctx) {
				return context.Cause(ctx)
			}
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func waitSchedulerRetry(ctx context.Context) bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunDue 最多执行每个已到期任务一次，避免休眠恢复后积压历史周期。
func (s Scheduler) RunDue(ctx context.Context, now time.Time) error {
	tasks, err := s.Store.Tasks()
	if err != nil {
		return err
	}
	var schedulerErrors []error
	for _, task := range tasks {
		if !task.Enabled || task.Status == StatusDisabled || task.Status == StatusPaused {
			continue
		}
		if !task.NextRunAt.IsZero() && task.NextRunAt.After(now) {
			continue
		}
		if !task.Schedule.Allows(now.In(task.location())) {
			record := RunRecord{
				ID:         newID("run"),
				TaskID:     task.ID,
				Trigger:    TriggerScheduled,
				Status:     StatusSkipped,
				Reason:     "当前时间不在允许执行的时段内",
				StartedAt:  now.UTC(),
				FinishedAt: now.UTC(),
			}
			if _, finishErr := s.Runner.finish(ctx, task, record, nil); finishErr != nil {
				schedulerErrors = append(schedulerErrors, finishErr)
			}
			continue
		}
		record, runErr := s.Runner.RunTask(ctx, task.ID, TriggerScheduled)
		if runErr != nil && record.ID == "" {
			schedulerErrors = append(schedulerErrors, runErr)
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
	}
	return errors.Join(schedulerErrors...)
}

func (s Scheduler) currentTime() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s Scheduler) nextDelay(now time.Time) (time.Duration, error) {
	tasks, err := s.Store.Tasks()
	if err != nil {
		return 0, err
	}
	delay := schedulerMaxPoll
	for _, task := range tasks {
		if !task.Enabled || task.Status == StatusDisabled || task.Status == StatusPaused {
			continue
		}
		if task.NextRunAt.IsZero() || !task.NextRunAt.After(now) {
			return 0, nil
		}
		delay = min(delay, timeUntil(task.NextRunAt, now))
	}
	return max(delay, time.Millisecond), nil
}
