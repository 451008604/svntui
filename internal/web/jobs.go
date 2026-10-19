package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"svntui/internal/domain"
	"svntui/internal/safepoint"
)

type Job struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Action      string    `json:"action"`
	State       string    `json:"state"`
	Summary     string    `json:"summary"`
	Output      string    `json:"output"`
	Error       *apiError `json:"error"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	SafePointID string    `json:"safepoint_id,omitempty"`
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PreviewID    string `json:"preview_id"`
		Confirm      bool   `json:"confirm"`
		Confirmation string `json:"confirmation,omitempty"`
	}
	if err := decode(w, r, &input); err != nil {
		s.fail(w, err)
		return
	}
	if !input.Confirm {
		s.fail(w, problem(400, "confirmation_required", "请先确认操作预览"))
		return
	}
	s.mu.Lock()
	preview, ok := s.previews[input.PreviewID]
	if !ok || preview.project.ID != r.PathValue("id") {
		s.mu.Unlock()
		s.fail(w, problem(404, "not_found", "预览不存在"))
		return
	}
	if preview.Risk == "high" && input.Confirmation != "YES" {
		s.mu.Unlock()
		s.fail(w, problem(400, "confirmation_required", "危险操作需要输入 YES"))
		return
	}
	// 同一预览只预留一次任务，并共享首次启动结果。
	if preview.jobID == "" {
		if s.closed {
			s.mu.Unlock()
			s.fail(w, problem(503, "closed", "服务正在关闭"))
			return
		}
		if time.Now().After(preview.ExpiresAt) {
			s.mu.Unlock()
			s.fail(w, problem(409, "stale_preview", "预览已过期，请重新预览"))
			return
		}
		if s.active[preview.project.ID] != "" {
			s.mu.Unlock()
			s.fail(w, problem(409, "busy", "工作副本有正在执行的操作"))
			return
		}
		id, err := secret()
		if err != nil {
			s.mu.Unlock()
			s.fail(w, err)
			return
		}
		preview.jobID = id
		preview.ready = make(chan struct{})
		s.active[preview.project.ID] = id
		s.writes.Add(1)
		go s.runJob(preview)
	}
	ready := preview.ready
	s.mu.Unlock()
	select {
	case <-ready:
	case <-r.Context().Done():
		return
	}
	s.mu.Lock()
	err := preview.startErr
	job := s.jobs[preview.jobID]
	s.mu.Unlock()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.reply(w, http.StatusAccepted, job)
}

func (s *Server) runJob(preview *frozenPreview) {
	defer s.writes.Done()
	ctx := context.Background()
	job := Job{ID: preview.jobID, ProjectID: preview.project.ID, Action: preview.Action, State: "running", Summary: preview.Summary, StartedAt: time.Now().UTC()}
	started := false
	check := func() error {
		// 写锁内重新校验项目、路径和全部内容，检查通过才发布任务。
		current, err := s.project(preview.project.ID)
		if err != nil {
			return err
		}
		if current.Path != preview.project.Path {
			return problem(409, "stale_preview", "项目绑定已变化，请重新预览")
		}
		if _, err := normalizeTargets(current.Path, preview.request.Targets); err != nil {
			return err
		}
		if preview.Action == "restore" {
			if _, err := s.scopedSafePoint(ctx, current, preview.input.SafePointID); err != nil {
				return err
			}
		}
		fingerprint, err := s.requestFingerprint(ctx, current, preview.request)
		if err != nil {
			return err
		}
		if fingerprint != preview.fingerprint || time.Now().After(preview.ExpiresAt) {
			return problem(409, "stale_preview", "工作副本内容或 SVN 状态已变化，请重新预览")
		}
		s.mu.Lock()
		started = true
		s.jobs[job.ID] = job
		s.jobOrder = append(s.jobOrder, job.ID)
		s.publishLocked(job)
		close(preview.ready)
		s.mu.Unlock()
		return nil
	}
	var result domain.ActionResult
	var err error
	if preview.Action == "restore" {
		var restored safepoint.RestoreResult
		restored, err = s.opts.SafePoints.RestoreWithPrecondition(ctx, preview.input.SafePointID, preview.project.Path, safepoint.RestoreOptions{CreateBackup: true}, check)
		result.Summary = "SafePoint 已恢复"
		result.Output = strings.Join(restored.Warnings, "\n")
		job.SafePointID = restored.BackupID
	} else {
		result, err = s.actions.ExecuteWithPrecondition(ctx, preview.request, check)
	}
	if !started {
		s.mu.Lock()
		if err != nil {
			preview.startErr = errorInfo(err)
		}
		if preview.startErr == nil {
			preview.startErr = problem(500, "internal", "操作未通过启动检查")
		}
		delete(s.active, preview.project.ID)
		close(preview.ready)
		s.mu.Unlock()
		return
	}
	job.Summary = result.Summary
	job.Output = result.Output
	// 提交、草稿持久化与状态刷新分别记录结果，刷新失败不能撤销已完成的提交。
	mutationErr := err
	var persistenceErr error
	if mutationErr == nil && preview.Action == "commit" {
		if clearErr := s.clearDraft(preview.project, preview.identity); clearErr != nil {
			persistenceErr = stageError("提交成功，但保存草稿清除状态失败", clearErr)
		}
	}
	_, refreshErr := s.workspace(ctx, preview.project, false)
	if refreshErr != nil {
		refreshErr = stageError("操作后刷新工作副本状态失败", refreshErr)
	}
	err = errors.Join(stageError("SVN 写入失败", mutationErr), persistenceErr, refreshErr)
	if len(job.Output) > outputLimit {
		job.Output = boundedJobOutput(job.Output)
		err = errors.Join(err, problem(413, "output_limit", "任务输出超过 1 MiB，已截断显示"))
	}
	if err != nil {
		job.State = "failed"
		job.Error = errorInfo(err)
		if job.Summary == "" {
			job.Summary = "操作失败，请检查工作副本当前状态"
		}
	} else {
		job.State = "succeeded"
	}
	job.FinishedAt = time.Now().UTC()
	if job.SafePointID == "" && preview.Action == "revert" && s.opts.SafePoints != nil {
		if manifests, listErr := s.opts.SafePoints.List(); listErr == nil {
			for _, manifest := range manifests {
				if sameRoot(preview.project.Path, manifest.WorkingCopy) && !manifest.CreatedAt.Before(job.StartedAt) {
					job.SafePointID = manifest.ID
					break
				}
			}
		}
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	delete(s.active, preview.project.ID)
	s.publishLocked(job)
	s.pruneJobsLocked()
	s.mu.Unlock()
}
func (s *Server) pruneJobsLocked() {
	finished := 0
	for _, id := range s.jobOrder {
		if s.jobs[id].State != "running" {
			finished++
		}
	}
	for finished > 100 {
		index := slices.IndexFunc(s.jobOrder, func(id string) bool { return s.jobs[id].State != "running" })
		if index < 0 {
			break
		}
		id := s.jobOrder[index]
		delete(s.jobs, id)
		s.jobOrder = slices.Delete(s.jobOrder, index, index+1)
		for key, p := range s.previews {
			if p.jobID == id {
				delete(s.previews, key)
			}
		}
		finished--
	}
}
func (s *Server) publishLocked(job Job) {
	for subscriber := range s.subscribers {
		select {
		case subscriber <- job:
		default:
			// 慢客户端重新连接后会获取完整快照，避免静默丢失最终状态。
			delete(s.subscribers, subscriber)
			close(subscriber)
		}
	}
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	result := make([]Job, 0, len(s.jobs))
	for i := len(s.jobOrder) - 1; i >= 0; i-- {
		result = append(result, s.jobs[s.jobOrder[i]])
	}
	s.mu.Unlock()
	s.reply(w, 200, result)
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job, ok := s.jobs[r.PathValue("job")]
	s.mu.Unlock()
	if !ok {
		s.fail(w, problem(404, "not_found", "任务不存在"))
		return
	}
	s.reply(w, 200, job)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, problem(500, "stream_unavailable", "服务器不支持事件流"))
		return
	}
	channel := make(chan Job, 32)
	s.mu.Lock()
	s.subscribers[channel] = struct{}{}
	snapshot := make([]Job, 0, len(s.jobs))
	for _, id := range s.jobOrder {
		snapshot = append(snapshot, s.jobs[id])
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, channel); s.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(job Job) error {
		data, err := json.Marshal(job)
		if err != nil {
			return err
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err = fmt.Fprintf(w, "event: job\ndata: %s\n\n", data)
		if err == nil {
			flusher.Flush()
		}
		return err
	}
	for _, job := range snapshot {
		if send(job) != nil {
			return
		}
	}
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case job, ok := <-channel:
			if !ok {
				return
			}
			if send(job) != nil {
				return
			}
		case <-ticker.C:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-s.reads.Done():
			return
		}
	}
}
