package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/config"
	"svntui/internal/svn"
)

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Modified    int    `json:"modified"`
	Conflicts   int    `json:"conflicts"`
	Unversioned int    `json:"unversioned"`
	Error       string `json:"error,omitempty"`
}

func canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}
func (s *Server) configured() ([]Project, error) {
	cfg, err := s.opts.ConfigStore.Load()
	if err != nil {
		return nil, err
	}
	projects := slices.Clone(cfg.Projects)
	for _, path := range cfg.Recent {
		if !slices.ContainsFunc(projects, func(p config.Project) bool { return p.Path == path }) {
			projects = append(projects, config.Project{Name: filepath.Base(path), Path: path})
		}
	}
	result := make([]Project, 0, len(projects))
	seen := make(map[string]bool)
	for _, p := range projects {
		root, err := canonical(p.Path)
		if err != nil {
			root, _ = filepath.Abs(p.Path)
		}
		id := projectID(root)
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, Project{ID: id, Name: p.Name, Path: root})
	}
	return result, nil
}
func (s *Server) project(id string) (Project, error) {
	projects, err := s.configured()
	if err != nil {
		return Project{}, err
	}
	for _, p := range projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, problem(404, "not_found", "项目不存在")
}
func (s *Server) withProject(w http.ResponseWriter, r *http.Request, f func(context.Context, Project) (any, error)) {
	ctx, done, err := s.requestContext(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer done()
	p, err := s.project(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	// 再次确认配置路径仍为已登记的真正工作副本根目录。
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err == nil {
		root, rootErr := canonical(info.WCAbsolute)
		if rootErr != nil || root != p.Path {
			err = problem(409, "project_changed", "工作副本根目录已变化，请重新登记")
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	data, err := f(ctx, p)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.reply(w, 200, data)
}
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	ctx, done, err := s.requestContext(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer done()
	projects, err := s.configured()
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range projects {
		entries, err := s.opts.Client.Status(ctx, projects[i].Path, false, false)
		if err != nil {
			projects[i].Error = err.Error()
			continue
		}
		for _, e := range entries {
			if e.IsVersionedDirty() {
				projects[i].Modified++
			}
			if e.IsConflict() {
				projects[i].Conflicts++
			}
			if e.Item == "unversioned" {
				projects[i].Unversioned++
			}
		}
	}
	s.reply(w, 200, projects)
}
func (s *Server) registerProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if strings.TrimSpace(body.Path) == "" || strings.Contains(body.Path, "://") {
		s.fail(w, problem(400, "invalid_argument", "请输入本地工作副本路径"))
		return
	}
	ctx, done, err := s.requestContext(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer done()
	path, err := canonical(body.Path)
	if err != nil {
		s.fail(w, problem(400, "invalid_argument", err.Error()))
		return
	}
	info, err := s.opts.Client.Info(ctx, path)
	if err != nil {
		s.fail(w, err)
		return
	}
	if info.WCAbsolute == "" {
		s.fail(w, problem(400, "invalid_argument", "路径不是本地工作副本"))
		return
	}
	path, err = canonical(info.WCAbsolute)
	if err != nil {
		s.fail(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = filepath.Base(path)
	}
	err = s.opts.ConfigStore.Update(func(cfg *config.Config) error { return cfg.AddProject(config.Project{Name: name, Path: path}) })
	if err != nil {
		s.fail(w, problem(400, "invalid_argument", err.Error()))
		return
	}
	s.reply(w, 200, Project{ID: projectID(path), Name: name, Path: path})
}
func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.project(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.mu.Lock()
	busy := s.active[p.ID] != ""
	if busy {
		s.mu.Unlock()
		s.fail(w, problem(409, "busy", "项目有正在执行的操作"))
		return
	}
	same := func(path string) bool {
		root, err := canonical(path)
		if err != nil {
			root, _ = filepath.Abs(path)
		}
		return projectID(root) == p.ID
	}
	err = s.opts.ConfigStore.Update(func(cfg *config.Config) error {
		cfg.Projects = slices.DeleteFunc(cfg.Projects, func(p config.Project) bool { return same(p.Path) })
		cfg.Recent = slices.DeleteFunc(cfg.Recent, same)
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.reply(w, 200, struct{}{})
}

// 拒绝元数据和符号链接父目录；最终链接本身仍可检查或删除。
func safeTarget(root, target string) (string, error) {
	if target == "" {
		target = "."
	}
	if strings.ContainsRune(target, 0) || filepath.IsAbs(target) || strings.Contains(target, "://") || strings.Contains(target, "\\") {
		return "", problem(400, "invalid_path", "目标必须是项目内相对路径")
	}
	target = filepath.Clean(filepath.FromSlash(target))
	if !filepath.IsLocal(target) {
		return "", problem(400, "invalid_path", "目标超出项目范围")
	}
	components := strings.Split(target, string(filepath.Separator))
	current := root
	for i, part := range components {
		if strings.EqualFold(part, ".svn") {
			return "", problem(403, "invalid_path", "禁止访问 SVN 元数据")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.IsDir() && current != root {
			if _, err := os.Stat(filepath.Join(current, ".svn")); err == nil {
				return "", problem(403, "invalid_path", "目标属于嵌套工作副本，请单独登记项目")
			}
		}
		if info.Mode()&os.ModeSymlink != 0 && i < len(components)-1 {
			return "", problem(403, "invalid_path", "目标父目录为符号链接，已拒绝穿越")
		}
	}
	return target, nil
}
func normalizeTargets(root string, targets []string) ([]string, error) {
	if len(targets) > 1000 {
		return nil, problem(400, "invalid_argument", "一次最多选择 1000 个路径")
	}
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		if target == "" {
			return nil, problem(400, "invalid_path", "目标路径不能为空")
		}
		path, err := safeTarget(root, target)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(result, path) {
			result = append(result, path)
		}
	}
	return result, nil
}
func infoIdentity(info svn.Info) string { return info.UUID + "\n" + info.RootURL + "\n" + info.URL }
