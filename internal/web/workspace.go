package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"svntui/internal/service"
	"svntui/internal/svn"
)

type StatusEntry struct {
	Path           string `json:"path"`
	Item           string `json:"item"`
	Props          string `json:"props"`
	ReposItem      string `json:"repos_item"`
	ReposProps     string `json:"repos_props"`
	Changelist     string `json:"changelist"`
	TreeConflicted bool   `json:"tree_conflicted"`
	Revision       int64  `json:"revision"`
}
type Workspace struct {
	Entries  []StatusEntry `json:"entries"`
	URL      string        `json:"url"`
	Root     string        `json:"root"`
	Revision int64         `json:"revision"`
}

func (s *Server) workspace(ctx context.Context, p Project, remote bool) (Workspace, error) {
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err != nil {
		return Workspace{}, err
	}
	entries, err := s.opts.Client.Status(ctx, p.Path, remote, true)
	if err != nil {
		return Workspace{}, err
	}
	result := Workspace{Entries: make([]StatusEntry, 0, len(entries)), URL: info.URL, Root: p.Path, Revision: info.Revision}
	for _, e := range entries {
		result.Entries = append(result.Entries, StatusEntry{filepath.ToSlash(e.Path), e.Item, e.Properties, e.RepositoryItem, e.RepositoryProps, e.Changelist, e.TreeConflicted, e.Revision})
	}
	return result, nil
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		return s.workspace(ctx, p, r.URL.Query().Get("remote") == "1")
	})
}
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		targets, err := normalizeTargets(p.Path, r.URL.Query()["target"])
		if err != nil {
			return nil, err
		}
		if len(targets) > 0 {
			entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
			if err != nil {
				return nil, err
			}
			for _, target := range targets {
				if !slices.ContainsFunc(entries, func(e svn.StatusEntry) bool {
					return apiPath(e.Path) == target && (e.Item == "unversioned" || e.Item == "ignored")
				}) {
					continue
				}
				info, err := os.Lstat(filepath.Join(p.Path, target))
				if err != nil {
					return nil, err
				}
				if info.Mode().IsRegular() && info.Size() > 512*1024 {
					return nil, problem(413, "output_limit", "未版本化文件超过 512 KiB 预览上限，请使用外部工具")
				}
			}
		}
		text, err := s.actions.WorkspaceDiff(ctx, p.Path, nativeTargets(targets))
		if err != nil {
			return nil, err
		}
		if int64(len(text)) > s.opts.Client.MaxOutputBytes {
			return nil, problem(413, "output_limit", "差异超过 SVN 输出上限，请缩小选择范围")
		}
		return struct {
			Text string `json:"text"`
		}{text}, nil
	})
}

type HistoryPath struct {
	Action           string `json:"action"`
	Path             string `json:"path"`
	CopyFromPath     string `json:"copy_from_path,omitempty"`
	CopyFromRevision int64  `json:"copy_from_revision,omitzero"`
}
type HistoryEntry struct {
	Revision int64         `json:"revision"`
	Author   string        `json:"author"`
	Date     time.Time     `json:"date"`
	Message  string        `json:"message"`
	Paths    []HistoryPath `json:"paths"`
}
type History struct {
	Entries      []HistoryEntry `json:"entries"`
	NextRevision int64          `json:"next_revision"`
	HasMore      bool           `json:"has_more"`
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		q := r.URL.Query()
		target, err := safeTarget(p.Path, q.Get("target"))
		if err != nil {
			return nil, err
		}
		opts := svn.LogOptions{Limit: 100, Author: q.Get("author"), Search: q.Get("search")}
		if value := q.Get("limit"); value != "" {
			opts.Limit, err = strconv.Atoi(value)
			if err != nil || opts.Limit < 1 || opts.Limit > 500 {
				return nil, problem(400, "invalid_argument", "历史页大小应为 1 至 500")
			}
		}
		if value := q.Get("before_revision"); value != "" {
			opts.StartRevision, err = strconv.ParseInt(value, 10, 64)
			if err != nil || opts.StartRevision < 1 {
				return nil, problem(400, "invalid_argument", "无效历史游标")
			}
		}
		opts.Since, err = service.ParseHistoryDate(q.Get("since"), false)
		if err != nil {
			return nil, problem(400, "invalid_argument", err.Error())
		}
		opts.Until, err = service.ParseHistoryDate(q.Get("until"), true)
		if err != nil {
			return nil, problem(400, "invalid_argument", err.Error())
		}
		page, err := s.opts.Client.LogWithOptions(ctx, filepath.Join(p.Path, target), opts)
		if err != nil {
			return nil, err
		}
		result := History{Entries: make([]HistoryEntry, 0, len(page.Entries)), NextRevision: page.NextRevision, HasMore: page.HasMore}
		for _, e := range page.Entries {
			entry := HistoryEntry{Revision: e.Revision, Author: e.Author, Date: e.Date, Message: e.Message, Paths: make([]HistoryPath, 0, len(e.Paths))}
			for _, path := range e.Paths {
				entry.Paths = append(entry.Paths, HistoryPath{path.Action, path.Path, path.CopyFromPath, path.CopyFromRev})
			}
			result.Entries = append(result.Entries, entry)
		}
		return result, nil
	})
}
func (s *Server) historyDiff(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if err != nil || revision < 1 {
			return nil, problem(400, "invalid_argument", "修订必须是正整数")
		}
		target, err := safeTarget(p.Path, r.URL.Query().Get("target"))
		if err != nil {
			return nil, err
		}
		text, err := s.opts.Client.RevisionDiff(ctx, p.Path, filepath.FromSlash(target), revision)
		if err != nil {
			return nil, err
		}
		if int64(len(text)) > s.opts.Client.MaxOutputBytes {
			return nil, problem(413, "output_limit", fmt.Sprintf("r%d 差异超过 SVN 输出上限", revision))
		}
		return struct {
			Text string `json:"text"`
		}{text}, nil
	})
}
