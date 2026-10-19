package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"unicode/utf8"

	"svntui/internal/svn"
)

type Conflict struct {
	Path    string   `json:"path"`
	Kinds   []string `json:"kinds"`
	Base    string   `json:"base"`
	Mine    string   `json:"mine"`
	Theirs  string   `json:"theirs"`
	Detail  string   `json:"detail"`
	Accepts []string `json:"accepts"`
}

func accepts(e svn.StatusEntry) []string {
	if e.TreeConflicted || e.Properties == "conflicted" {
		return []string{"working"}
	}
	return []string{"working", "mine-full", "theirs-full", "base"}
}
func (s *Server) conflicts(w http.ResponseWriter, r *http.Request) {
	s.withProject(w, r, func(ctx context.Context, p Project) (any, error) {
		entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
		if err != nil {
			return nil, err
		}
		result := make([]Conflict, 0)
		for _, e := range entries {
			if !e.IsConflict() {
				continue
			}
			c := Conflict{Path: filepath.ToSlash(e.Path), Kinds: []string{}, Accepts: accepts(e)}
			if e.Item == "conflicted" {
				c.Kinds = append(c.Kinds, "text")
			}
			if e.Properties == "conflicted" {
				c.Kinds = append(c.Kinds, "property")
			}
			if e.TreeConflicted {
				c.Kinds = append(c.Kinds, "tree")
			}
			c.Base = artifact(p.Path, e.ConflictOld)
			// SVN prev-wc-file 为本地版本，cur-base-file 为更新后的远端基线。
			c.Mine = artifact(p.Path, e.ConflictNew)
			c.Theirs = artifact(p.Path, e.ConflictWorking)
			c.Detail = fmt.Sprintf("状态: %s / %s；操作: %s\n旧侧: %s %s@r%d\n新侧: %s %s@r%d", e.Item, e.Properties, e.ConflictOperation, e.ConflictLeft.Kind, e.ConflictLeft.Path, e.ConflictLeft.Revision, e.ConflictRight.Kind, e.ConflictRight.Path, e.ConflictRight.Revision)
			if e.TreeConflicted || e.Properties == "conflicted" {
				c.Detail += "\n请先在外部工具中整理内容、属性或目录，再选择 working。"
			}
			result = append(result, c)
		}
		return result, nil
	})
}
func artifact(root, path string) string {
	if path == "" {
		return "[此冲突没有可用的文本制品]"
	}
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "[制品路径不可用]"
		}
		path = relative
	}
	target, err := safeTarget(root, path)
	if err != nil {
		return "[制品位于安全范围之外]"
	}
	info, err := os.Lstat(filepath.Join(root, target))
	if err != nil {
		return "[制品不可用: " + err.Error() + "]"
	}
	if !info.Mode().IsRegular() {
		return "[制品不是普通文本文件]"
	}
	// os.Root 同时保护检查与打开之间被替换成链接的情况。
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "[无法打开工作副本]"
	}
	defer dir.Close()
	file, err := dir.Open(target)
	if err != nil {
		return "[制品不可用: " + err.Error() + "]"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	if err != nil {
		return "[读取制品失败]"
	}
	if len(data) > 256*1024 {
		return "[制品超过 256 KiB，请使用外部工具]"
	}
	if bytes.ContainsRune(data, 0) || !utf8.Valid(data) {
		return "[二进制制品，请使用外部工具]"
	}
	return string(data)
}
