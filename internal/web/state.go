package web

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/storage"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

type Draft struct {
	Targets  []string `json:"targets"`
	Message  string   `json:"message"`
	Revision int64    `json:"revision"`
}
type storedDraft struct {
	Draft
	Identity  string `json:"identity"`
	ProjectID string `json:"project_id"`
}

func (s *Server) draftPath(p Project) string {
	return filepath.Join(s.opts.StateDir, "web", "drafts", p.ID+".json")
}

// 仅保护本地草稿事务；锁内不运行 SVN，也不获取配置或工作副本锁。
func (s *Server) lockDraft(ctx context.Context, p Project) (*wclock.Lock, error) {
	// 锁文件名只取项目标识；状态目录的大小写、符号链接等别名由文件系统归一。
	lockDir := filepath.Join(s.opts.StateDir, "web", "draft-locks")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		lock, err := wclock.AcquireKey(lockDir, p.ID, "draft")
		if !errors.Is(err, wclock.ErrBusy) {
			return lock, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, problem(409, "draft_busy", "草稿正在保存，请重试")
	}
	return nil, ctx.Err()
}

func (s *Server) loadDraft(p Project, identity string) (storedDraft, error) {
	draft := storedDraft{Draft: Draft{Targets: []string{}}}
	if err := storage.LoadJSON(s.draftPath(p), &draft); err != nil {
		return draft, err
	}
	if draft.ProjectID != p.ID || draft.Identity != identity {
		draft = storedDraft{Draft: Draft{Targets: []string{}}, ProjectID: p.ID, Identity: identity}
	}
	return draft, nil
}
func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		info, err := s.opts.Client.Info(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		lock, err := s.lockDraft(ctx, p)
		if err != nil {
			return nil, err
		}
		draft, err := s.loadDraft(p, infoIdentity(info))
		_ = lock.Release()
		if err != nil {
			return nil, err
		}
		entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
		if err != nil {
			return nil, err
		}
		selected := make([]string, 0, len(draft.Targets))
		for _, target := range draft.Targets {
			if _, err := safeTarget(p.Path, target); err != nil {
				continue
			}
			if slices.ContainsFunc(entries, func(e svn.StatusEntry) bool { return apiPath(e.Path) == target }) {
				selected = append(selected, target)
			}
		}
		draft.Targets = selected
		return draft.Draft, nil
	})
}
func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Targets  []string `json:"targets"`
		Message  string   `json:"message"`
		Revision *int64   `json:"revision"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Revision == nil || *body.Revision < 0 {
		s.fail(w, problem(400, "invalid_argument", "保存草稿需要当前 revision"))
		return
	}
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		targets, err := normalizeTargets(p.Path, body.Targets)
		if err != nil {
			return nil, err
		}
		info, err := s.opts.Client.Info(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		lock, err := s.lockDraft(ctx, p)
		if err != nil {
			return nil, err
		}
		defer lock.Release()
		// 读取、比较版本和原子替换必须处于同一个跨进程事务。
		current, err := s.loadDraft(p, infoIdentity(info))
		if err != nil {
			return nil, err
		}
		if current.Revision != *body.Revision {
			return nil, problem(409, "stale_draft", "草稿已变化，请重新载入")
		}
		current.Draft = Draft{Targets: targets, Message: body.Message, Revision: current.Revision + 1}
		if err := storage.SaveJSON(s.draftPath(p), current); err != nil {
			return nil, err
		}
		return current.Draft, nil
	})
}

// 持久保存空草稿和新版本，防止已发送的旧自动保存重新填回提交内容。
func (s *Server) clearDraft(p Project, identity string) error {
	// SVN 执行已释放共享工作副本锁，再等待短时草稿事务完成。
	lock, err := s.lockDraft(context.Background(), p)
	if err != nil {
		return err
	}
	defer lock.Release()
	// 提交结果已确认仓库身份，清除草稿不再依赖可能失败的后续 SVN 读取。
	current := storedDraft{}
	if err := storage.LoadJSON(s.draftPath(p), &current); err != nil {
		return err
	}
	current.ProjectID = p.ID
	current.Identity = identity
	current.Draft = Draft{Targets: []string{}, Revision: current.Revision + 1}
	return storage.SaveJSON(s.draftPath(p), current)
}

func (s *Server) scopedSafePoint(ctx context.Context, p Project, id string) (safepoint.Manifest, error) {
	if s.opts.SafePoints == nil {
		return safepoint.Manifest{}, problem(503, "unavailable", "SafePoint 服务不可用")
	}
	manifest, err := s.opts.SafePoints.Inspect(id)
	if err != nil {
		return manifest, err
	}
	if !sameRoot(p.Path, manifest.WorkingCopy) {
		return safepoint.Manifest{}, problem(404, "not_found", "项目中没有此 SafePoint")
	}
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err != nil {
		return manifest, err
	}
	if manifest.RepositoryUUID != info.UUID || manifest.RootURL != info.RootURL || manifest.URL != info.URL {
		return safepoint.Manifest{}, problem(404, "not_found", "SafePoint 属于其他仓库身份")
	}
	return manifest, nil
}
func sameRoot(a, b string) bool {
	canonicalA, err := canonical(a)
	if err != nil {
		return false
	}
	canonicalB, err := canonical(b)
	return err == nil && canonicalA == canonicalB
}

type SafePointSummary struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
}

func (s *Server) safePoints(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		result := make([]SafePointSummary, 0)
		if s.opts.SafePoints == nil {
			return result, nil
		}
		all, err := s.opts.SafePoints.List()
		if err != nil {
			return nil, err
		}
		info, err := s.opts.Client.Info(ctx, p.Path)
		if err != nil {
			return nil, err
		}
		for _, m := range all {
			if sameRoot(p.Path, m.WorkingCopy) && m.RepositoryUUID == info.UUID && m.RootURL == info.RootURL && m.URL == info.URL {
				result = append(result, SafePointSummary{ID: m.ID, CreatedAt: m.CreatedAt, Reason: m.Reason})
			}
		}
		return result, nil
	})
}
func (s *Server) safePoint(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		return s.scopedSafePoint(ctx, p, r.PathValue("sid"))
	})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		result := make([]operation.Record, 0)
		if s.opts.Operations.Path == "" {
			return result, nil
		}
		records, err := s.opts.Operations.Last(1000)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if record.Directory == "" {
				continue
			}
			root, err := canonical(record.Directory)
			if err != nil {
				continue
			}
			relative, err := filepath.Rel(p.Path, root)
			if err != nil || !filepath.IsLocal(relative) {
				continue
			}
			result = append(result, record)
			if len(result) == 100 {
				break
			}
		}
		return result, nil
	})
}
