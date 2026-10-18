package automation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"svntui/internal/svn"
	"svntui/internal/wclock"
)

const maxNetworkBackoff = 6 * time.Hour

type SVNClient interface {
	Info(context.Context, string) (svn.Info, error)
	Status(context.Context, string, bool, bool) ([]svn.StatusEntry, error)
	Update(context.Context, string, bool) (svn.Result, error)
}

// NewTask 读取实际工作副本身份并生成默认任务。
func NewTask(ctx context.Context, client SVNClient, name, workingCopy string) (Task, error) {
	if client == nil {
		return Task{}, errors.New("SVN 客户端不能为空")
	}
	info, err := client.Info(ctx, workingCopy)
	if err != nil {
		return Task{}, fmt.Errorf("检查工作副本: %w", err)
	}
	task := Task{
		Name:           name,
		Enabled:        true,
		Status:         StatusScheduled,
		WorkingCopy:    info.WCAbsolute,
		RepositoryUUID: info.UUID,
		RootURL:        info.RootURL,
		BranchURL:      info.URL,
		Interval:       Duration(defaultInterval),
		LastRevision:   info.Revision,
	}
	if task.WorkingCopy == "" {
		task.WorkingCopy = workingCopy
	}
	if err := task.Normalize(); err != nil {
		return Task{}, err
	}
	return task, nil
}

// Runner 对一个工作副本执行完整的校验、更新、持久化和通知流程。
type Runner struct {
	Store             Store
	SVN               SVNClient
	LockDir           string
	Webhooks          WebhookClient
	Now               func() time.Time
	Jitter            func(time.Duration) time.Duration
	beforeMarkRunning func()
}

func NewRunner(store Store, client SVNClient) Runner {
	return Runner{
		Store:   store,
		SVN:     client,
		LockDir: filepath.Join(filepath.Dir(store.Path), "working-copy-locks"),
	}
}

func (r Runner) RunTask(ctx context.Context, taskID string, trigger Trigger) (RunRecord, error) {
	task, err := r.Store.Task(taskID)
	if err != nil {
		return RunRecord{}, err
	}
	started := nowUTC(r.Now)
	record := RunRecord{
		ID:        newID("run"),
		TaskID:    task.ID,
		Trigger:   trigger,
		Status:    StatusRunning,
		StartedAt: started,
	}
	if !task.Enabled || task.Status == StatusDisabled {
		record.Status = StatusSkipped
		record.Reason = "任务已禁用"
		return r.finish(ctx, task, record, nil)
	}
	if task.Status == StatusPaused {
		record.Status = StatusSkipped
		record.Reason = "任务已暂停: " + task.PauseReason
		return r.finish(ctx, task, record, nil)
	}
	if r.SVN == nil {
		return RunRecord{}, errors.New("SVN 客户端不能为空")
	}

	lock, err := wclock.Acquire(r.lockDir(), task.WorkingCopy, "自动更新")
	if err != nil {
		if errors.Is(err, wclock.ErrBusy) {
			retryDelay := min(task.Interval.Duration(), time.Minute)
			if retryDelay <= 0 {
				retryDelay = time.Minute
			}
			location := task.location()
			if task.Schedule.Allows(started.In(location)) && !task.Schedule.Allows(started.Add(retryDelay).In(location)) {
				record.Status = StatusSkipped
				record.Reason = "工作副本在整个允许执行时段内一直被占用"
			} else {
				record.Status = StatusDeferred
				record.Reason = "工作副本正在执行其他写操作"
			}
			return r.finish(ctx, task, record, nil)
		}
		return RunRecord{}, err
	}
	defer lock.Release()
	if r.beforeMarkRunning != nil {
		r.beforeMarkRunning()
	}
	controlStatus := TaskStatus("")
	controlReason := ""
	deletedBeforeStart := false
	if err := r.Store.Update(func(state *State) error {
		index := slices.IndexFunc(state.Tasks, func(current Task) bool { return current.ID == task.ID })
		if index < 0 {
			deletedBeforeStart = true
			return nil
		}
		current := &state.Tasks[index]
		task = *current
		if !current.Enabled || current.Status == StatusDisabled || current.Status == StatusPaused {
			controlStatus = current.Status
			controlReason = current.PauseReason
			return nil
		}
		current.Status = StatusRunning
		return nil
	}); err != nil {
		return RunRecord{}, err
	}
	if deletedBeforeStart {
		record.Status = StatusSkipped
		record.Reason = "更新开始前任务已被删除"
		return r.finish(ctx, task, record, nil)
	}
	if controlStatus != "" {
		record.Status = StatusSkipped
		record.Reason = "更新开始前任务已被禁用"
		if controlStatus == StatusPaused {
			record.Reason = "更新开始前任务已被暂停: " + controlReason
		}
		return r.finish(ctx, task, record, nil)
	}

	if info, statErr := os.Stat(task.WorkingCopy); statErr != nil || !info.IsDir() {
		record.Status = StatusPaused
		record.Reason = "工作副本目录不可用"
		if statErr != nil {
			err = statErr
		} else {
			err = errors.New("工作副本路径不是目录")
		}
		return r.finish(ctx, task, record, err)
	}

	info, err := r.SVN.Info(ctx, task.WorkingCopy)
	if err != nil {
		record.Status = statusForSVNError(err, task.ConsecutiveFailures+1)
		record.Reason = "无法验证工作副本身份"
		record.ErrorKind = string(errorKind(err))
		return r.finish(ctx, task, record, err)
	}
	record.BeforeRevision = info.Revision
	if reason := identityMismatch(task, info); reason != "" {
		err = errors.New(reason)
		record.Status = StatusPaused
		record.Reason = reason
		return r.finish(ctx, task, record, err)
	}

	entries, err := r.SVN.Status(ctx, task.WorkingCopy, false, false)
	if err != nil {
		record.Status = statusForSVNError(err, task.ConsecutiveFailures+1)
		record.Reason = "无法检查工作副本状态"
		record.ErrorKind = string(errorKind(err))
		return r.finish(ctx, task, record, err)
	}
	if reason := unsafeWorkingCopy(entries, task.AllowSwitched); reason != "" {
		record.Status = StatusSkipped
		record.Reason = reason
		return r.finish(ctx, task, record, nil)
	}

	result, updateErr := r.SVN.Update(ctx, task.WorkingCopy, !task.UpdateExternals)
	changed, outputRevision := svn.ParseUpdateOutput(result.Stdout)
	for _, item := range changed {
		record.Changes = append(record.Changes, Change{Action: item.Action, Path: item.Path})
	}
	if outputRevision > 0 {
		record.AfterRevision = outputRevision
	} else {
		record.AfterRevision = info.Revision
	}
	if updateErr != nil {
		record.Status = statusForSVNError(updateErr, task.ConsecutiveFailures+1)
		record.Reason = "svn update 执行失败"
		record.ErrorKind = string(errorKind(updateErr))
		return r.finish(ctx, task, record, updateErr)
	}
	if slices.ContainsFunc(changed, func(item svn.ChangedPath) bool { return item.Action == "C" }) {
		err = errors.New("svn update 产生了冲突")
		record.Status = StatusPaused
		record.Reason = err.Error()
		record.ErrorKind = string(svn.ErrorConflict)
		return r.finish(ctx, task, record, err)
	}
	postStatus, statusErr := r.SVN.Status(ctx, task.WorkingCopy, false, false)
	if statusErr != nil {
		record.Status = statusForSVNError(statusErr, task.ConsecutiveFailures+1)
		record.Reason = "无法验证更新后的工作副本"
		record.ErrorKind = string(errorKind(statusErr))
		return r.finish(ctx, task, record, statusErr)
	}
	if slices.ContainsFunc(postStatus, func(entry svn.StatusEntry) bool { return entry.IsConflict() }) {
		err = errors.New("svn update 后仍有未解决的冲突")
		record.Status = StatusPaused
		record.Reason = err.Error()
		record.ErrorKind = string(svn.ErrorConflict)
		return r.finish(ctx, task, record, err)
	}
	if postInfo, infoErr := r.SVN.Info(ctx, task.WorkingCopy); infoErr == nil && postInfo.Revision > 0 {
		record.AfterRevision = postInfo.Revision
	}
	record.Status = StatusSucceeded
	if len(record.Changes) == 0 {
		record.Outcome = OutcomeNoChange
	} else {
		record.Outcome = OutcomeChanged
	}
	return r.finish(ctx, task, record, nil)
}

func (r Runner) PauseTask(ctx context.Context, taskID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "用户手动暂停"
	}
	var task Task
	if err := r.Store.Update(func(state *State) error {
		return updateTask(state, taskID, func(current *Task) {
			current.Status = StatusPaused
			current.PauseReason = reason
			current.NextRunAt = time.Time{}
			task = *current
		})
	}); err != nil {
		return err
	}
	r.notifyLifecycle(ctx, task, EventUpdatePaused)
	return nil
}

func (r Runner) ResumeTask(ctx context.Context, taskID string) error {
	now := nowUTC(r.Now)
	var task Task
	if err := r.Store.Update(func(state *State) error {
		return updateTask(state, taskID, func(current *Task) {
			current.Enabled = true
			current.Status = StatusScheduled
			current.PauseReason = ""
			current.ConsecutiveFailures = 0
			candidate := now.Add(current.Interval.Duration()).In(current.location())
			current.NextRunAt = current.Schedule.NextAllowed(candidate)
			task = *current
		})
	}); err != nil {
		return err
	}
	r.notifyLifecycle(ctx, task, EventUpdateResumed)
	return nil
}

func (r Runner) TestWebhook(ctx context.Context, webhookID string) WebhookDelivery {
	webhook, err := r.Store.Webhook(webhookID)
	if err != nil {
		return WebhookDelivery{EventID: newID("event"), Error: err.Error(), Finished: nowUTC(r.Now)}
	}
	event := WebhookEvent{
		EventID: newID("event"),
		Type:    EventUpdateSucceeded,
		Time:    nowUTC(r.Now),
		Task:    Task{ID: "webhook-test", Name: "Webhook 测试"},
	}
	return r.Webhooks.Send(ctx, webhook, event)
}

func (r Runner) finish(ctx context.Context, task Task, record RunRecord, cause error) (RunRecord, error) {
	record.FinishedAt = nowUTC(r.Now)
	if record.FinishedAt.Before(record.StartedAt) {
		record.FinishedAt = record.StartedAt
	}
	var webhook WebhookConfig
	var hasWebhook bool
	deleted := false
	if err := r.Store.Update(func(state *State) error {
		if err := updateTask(state, task.ID, func(current *Task) {
			applyRun(current, record, r.nextRun(*current, record))
			task = *current
		}); err != nil {
			if errors.Is(err, ErrTaskNotFound) {
				deleted = true
				appendRun(state, record)
				return nil
			}
			return err
		}
		appendRun(state, record)
		webhook, hasWebhook = taskWebhook(*state, task)
		return nil
	}); err != nil {
		return RunRecord{}, err
	}
	if deleted {
		return record, cause
	}

	eventType, notify := eventForRun(task, record)
	if hasWebhook && notify {
		event := WebhookEvent{EventID: newID("event"), Type: eventType, Time: record.FinishedAt, Task: task, Run: &record}
		delivery := r.Webhooks.Send(ctx, webhook, event)
		record.Webhook = &delivery
		_ = r.Store.Update(func(state *State) error {
			index := slices.IndexFunc(state.Runs, func(item RunRecord) bool { return item.ID == record.ID })
			if index >= 0 {
				state.Runs[index].Webhook = &delivery
			}
			return nil
		})
	}
	return record, cause
}

func applyRun(task *Task, record RunRecord, next time.Time) {
	wasDisabled := !task.Enabled || task.Status == StatusDisabled
	wasPaused := task.Status == StatusPaused
	pauseReason := task.PauseReason
	task.Status = record.Status
	task.LastRunAt = record.FinishedAt
	task.LastResult = record.Status
	task.LastOutcome = record.Outcome
	if record.AfterRevision > 0 {
		task.LastRevision = record.AfterRevision
	}
	if wasDisabled {
		task.Status = StatusDisabled
		task.NextRunAt = time.Time{}
		return
	}
	if wasPaused {
		task.Status = StatusPaused
		task.PauseReason = pauseReason
		task.NextRunAt = time.Time{}
		return
	}
	switch record.Status {
	case StatusSucceeded:
		task.ConsecutiveFailures = 0
		task.PauseReason = ""
	case StatusFailed:
		task.ConsecutiveFailures++
	case StatusPaused:
		task.ConsecutiveFailures++
		task.PauseReason = record.Reason
	}
	if record.Status == StatusPaused || record.Status == StatusDisabled {
		task.NextRunAt = time.Time{}
	} else {
		task.NextRunAt = next
	}
}

func (r Runner) nextRun(task Task, record RunRecord) time.Time {
	delay := task.Interval.Duration()
	if delay <= 0 {
		delay = defaultInterval
	}
	switch record.Status {
	case StatusDeferred:
		delay = min(delay, time.Minute)
	case StatusFailed:
		if record.ErrorKind == string(svn.ErrorNetwork) {
			delay = networkBackoff(delay, task.ConsecutiveFailures+1)
		}
	}
	if r.Jitter != nil {
		delay += max(r.Jitter(delay), 0)
	} else {
		delay += randomJitter(min(delay/20, 30*time.Second))
	}
	candidate := record.FinishedAt.Add(delay).In(task.location())
	return task.Schedule.NextAllowed(candidate)
}

func randomJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(limit)+1))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

func networkBackoff(interval time.Duration, failures int) time.Duration {
	delay := interval
	for range max(failures-1, 0) {
		if delay >= maxNetworkBackoff/2 {
			return maxNetworkBackoff
		}
		delay *= 2
	}
	return min(delay, maxNetworkBackoff)
}

func (r Runner) notifyLifecycle(ctx context.Context, task Task, eventType EventType) {
	state, err := r.Store.Load()
	if err != nil {
		return
	}
	webhook, ok := taskWebhook(state, task)
	if !ok {
		return
	}
	event := WebhookEvent{EventID: newID("event"), Type: eventType, Time: nowUTC(r.Now), Task: task}
	_ = r.Webhooks.Send(ctx, webhook, event)
}

func (r Runner) lockDir() string {
	if r.LockDir != "" {
		return r.LockDir
	}
	return filepath.Join(filepath.Dir(r.Store.Path), "working-copy-locks")
}

func identityMismatch(task Task, info svn.Info) string {
	switch {
	case task.RepositoryUUID != info.UUID:
		return "工作副本的仓库 UUID 已变化"
	case canonicalURL(task.RootURL) != canonicalURL(info.RootURL):
		return "工作副本的仓库根 URL 已变化"
	case canonicalURL(task.BranchURL) != canonicalURL(info.URL):
		return "工作副本的分支 URL 已变化"
	default:
		return ""
	}
}

func canonicalURL(value string) string { return strings.TrimRight(value, "/") }

func unsafeWorkingCopy(entries []svn.StatusEntry, allowSwitched bool) string {
	for _, entry := range entries {
		if entry.Switched && !allowSwitched {
			return "工作副本包含已切换的子树"
		}
		if entry.IsConflict() {
			return "工作副本包含未解决的冲突"
		}
		if entry.IsVersionedDirty() {
			return "工作副本包含版本化的本地修改"
		}
	}
	return ""
}

func statusForSVNError(err error, failures int) TaskStatus {
	kind := errorKind(err)
	switch kind {
	case svn.ErrorAuth, svn.ErrorConflict, svn.ErrorNotWorkingCopy, svn.ErrorPermission:
		return StatusPaused
	case svn.ErrorNetwork:
		if failures >= 3 {
			return StatusPaused
		}
		return StatusFailed
	default:
		if failures >= 3 {
			return StatusPaused
		}
		return StatusFailed
	}
}

func errorKind(err error) svn.ErrorKind {
	var commandErr *svn.CommandError
	if errors.As(err, &commandErr) {
		return commandErr.Kind
	}
	return svn.ErrorOther
}

func eventForRun(task Task, record RunRecord) (EventType, bool) {
	switch record.Status {
	case StatusSucceeded:
		if record.Outcome == OutcomeNoChange {
			return EventUpdateNoChange, task.Notifications.NoChange
		}
		return EventUpdateSucceeded, task.Notifications.Succeeded
	case StatusSkipped:
		return EventUpdateSkipped, true
	case StatusFailed:
		return EventUpdateFailed, true
	case StatusPaused:
		return EventUpdatePaused, true
	default:
		return "", false
	}
}
