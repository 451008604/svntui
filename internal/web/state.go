package web

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/storage"
	"svntui/internal/svn"
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
func (s *Server) loadDraft(ctx context.Context, p Project) (storedDraft, error) {
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err != nil {
		return storedDraft{}, err
	}
	draft := storedDraft{Draft: Draft{Targets: []string{}}}
	if err := storage.LoadJSON(s.draftPath(p), &draft); err != nil {
		return draft, err
	}
	if draft.ProjectID != p.ID || draft.Identity != infoIdentity(info) {
		draft = storedDraft{Draft: Draft{Targets: []string{}}, ProjectID: p.ID, Identity: infoIdentity(info)}
	}
	return draft, nil
}
func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		s.draftMu.Lock()
		defer s.draftMu.Unlock()
		draft, err := s.loadDraft(ctx, p)
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
		s.draftMu.Lock()
		defer s.draftMu.Unlock()
		current, err := s.loadDraft(ctx, p)
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
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
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
