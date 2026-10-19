package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"svntui/internal/domain"
	"svntui/internal/svn"
)

type PreviewRequest struct {
	Action      string   `json:"action"`
	Targets     []string `json:"targets"`
	Message     string   `json:"message,omitempty"`
	Changelist  string   `json:"changelist,omitempty"`
	NewPath     string   `json:"new_path,omitempty"`
	Revision    string   `json:"revision,omitempty"`
	Accept      string   `json:"accept,omitempty"`
	Remove      bool     `json:"remove,omitzero"`
	SafePointID string   `json:"safepoint_id,omitempty"`
}
type Preview struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Targets   []string  `json:"targets"`
	Summary   string    `json:"summary"`
	Output    string    `json:"output"`
	Risk      string    `json:"risk"`
	ExpiresAt time.Time `json:"expires_at"`
}
type frozenPreview struct {
	Preview
	project     Project
	request     domain.ActionRequest
	input       PreviewRequest
	identity    string
	fingerprint string
	jobID       string
	ready       chan struct{}
	startErr    error
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var input PreviewRequest
	if err := decode(w, r, &input); err != nil {
		s.fail(w, err)
		return
	}
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		frozen, err := s.buildPreview(ctx, p, input)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, old := range s.previews {
			if (old.jobID == "" || old.startErr != nil) && time.Now().After(old.ExpiresAt) {
				delete(s.previews, id)
			}
		}
		if len(s.previews) >= 200 {
			return nil, problem(429, "preview_limit", "预览过多，请等待旧预览过期")
		}
		s.previews[frozen.ID] = frozen
		return frozen.Preview, nil
	})
}
func (s *Server) buildPreview(ctx context.Context, p Project, input PreviewRequest) (*frozenPreview, error) {
	if !slices.Contains([]string{"commit", "add", "delete", "move", "revert", "update", "changelist", "resolve", "cleanup", "restore"}, input.Action) {
		return nil, problem(400, "invalid_argument", "不支持的操作")
	}
	targets, err := normalizeTargets(p.Path, input.Targets)
	if err != nil {
		return nil, err
	}
	input.Targets = targets
	request := domain.ActionRequest{WorkingCopy: p.Path, Action: domain.Action(input.Action), Targets: targets}
	switch input.Action {
	case "commit":
		if strings.TrimSpace(input.Message) == "" {
			return nil, problem(400, "invalid_argument", "请输入提交说明")
		}
		entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 && input.Changelist != "" {
			for _, e := range entries {
				if e.Changelist == input.Changelist && e.IsVersionedDirty() {
					targets = append(targets, apiPath(e.Path))
				}
			}
		}
		if len(targets) == 0 {
			return nil, problem(400, "invalid_argument", "请明确选择提交路径或非空变更分组")
		}
		targets, err = normalizeTargets(p.Path, targets)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			index := slices.IndexFunc(entries, func(e svn.StatusEntry) bool { return apiPath(e.Path) == target })
			if index < 0 || !entries[index].IsVersionedDirty() || entries[index].IsConflict() || entries[index].Item == "missing" {
				return nil, problem(409, "invalid_state", "所选路径当前不可提交: "+target)
			}
		}
		request.Targets = targets
		request.Arguments = []string{input.Message}
	case "move":
		if len(targets) != 1 || input.NewPath == "" {
			return nil, problem(400, "invalid_argument", "移动需要一个源路径和目标路径")
		}
		destination, err := safeTarget(p.Path, input.NewPath)
		if err != nil {
			return nil, err
		}
		if targets[0] == "." || destination == "." {
			return nil, problem(400, "invalid_argument", "不能移动工作副本根目录")
		}
		request.Arguments = []string{targets[0], destination}
	case "changelist":
		if input.Remove {
			request.Arguments = []string{"--remove"}
		} else {
			if strings.TrimSpace(input.Changelist) == "" || strings.HasPrefix(input.Changelist, "-") {
				return nil, problem(400, "invalid_argument", "请输入有效变更分组名")
			}
			request.Arguments = []string{input.Changelist}
		}
	case "resolve":
		accept := input.Accept
		if accept == "" {
			accept = "working"
		}
		entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			index := slices.IndexFunc(entries, func(e svn.StatusEntry) bool { return apiPath(e.Path) == target })
			if index < 0 || !entries[index].IsConflict() || !slices.Contains(accepts(entries[index]), accept) {
				return nil, problem(400, "invalid_argument", "当前冲突不支持此解决方式: "+target)
			}
		}
		request.Arguments = []string{accept}
	case "update":
		if input.Revision != "" && !validRevision(input.Revision) {
			return nil, problem(400, "invalid_argument", "更新修订应为非负整数或 HEAD")
		}
		request.Revision = input.Revision
	case "delete", "revert":
		if slices.Contains(targets, ".") {
			return nil, problem(400, "invalid_argument", "危险操作不能选择工作副本根目录")
		}
	case "restore":
		if len(targets) > 0 {
			return nil, problem(400, "invalid_argument", "SafePoint 恢复覆盖其完整记录范围，不能传入部分路径")
		}
		manifest, err := s.scopedSafePoint(ctx, p, input.SafePointID)
		if err != nil {
			return nil, err
		}
		validation, err := s.opts.SafePoints.Validate(ctx, manifest.ID, p.Path)
		if err != nil {
			return nil, err
		}
		if !validation.Valid {
			return nil, problem(409, "conflict", "SafePoint 与当前基础修订不匹配")
		}
		request.Targets = []string{"."}
	}
	if input.Action != "restore" {
		request, err = s.actions.NormalizeAction(request)
		if err != nil {
			return nil, problem(400, "invalid_argument", err.Error())
		}
	}
	// 服务规范化可能产生原生分隔符；冻结与返回值再次统一为 API 路径。
	for i, target := range request.Targets {
		request.Targets[i] = apiPath(target)
	}
	if request.Action == domain.ActionMove {
		request.Arguments = slices.Clone(request.Targets)
	}
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	before, err := s.requestFingerprint(ctx, p, request)
	if err != nil {
		return nil, err
	}
	output := s.actions.PreviewAction(request)
	if input.Action == "update" {
		preview, err := s.actions.Execute(ctx, request)
		if err != nil {
			return nil, err
		}
		output = preview.Output
		request.Arguments = []string{"--apply"}
	}
	if input.Action == "restore" {
		output = "恢复 SafePoint " + input.SafePointID + "；恢复前将自动保护当前修改。"
	}
	if len(output) > outputLimit {
		return nil, problem(413, "output_limit", "预览输出超过 1 MiB，请缩小操作范围")
	}
	after, err := s.requestFingerprint(ctx, p, request)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, problem(409, "stale_preview", "创建预览期间工作副本发生变化，请重新预览")
	}
	id, err := secret()
	if err != nil {
		return nil, err
	}
	risk := "normal"
	if slices.Contains([]string{"delete", "revert", "restore"}, input.Action) {
		risk = "high"
	}
	summary := request.Action.DisplayName() + "操作预览"
	if input.Action == "restore" {
		summary = "SafePoint 恢复预览"
	}
	return &frozenPreview{Preview: Preview{ID: id, Action: input.Action, Targets: request.Targets, Summary: summary, Output: output, Risk: risk, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}, project: p, request: request, input: input, identity: infoIdentity(info), fingerprint: after}, nil
}
func validRevision(value string) bool {
	if value == "HEAD" {
		return true
	}
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
