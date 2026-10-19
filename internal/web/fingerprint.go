package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/domain"
	"svntui/internal/svn"
)

type fingerprintProperty struct {
	Name     string `xml:"name,attr"`
	Encoding string `xml:"encoding,attr"`
	Value    string `xml:",chardata"`
}
type fingerprintProperties struct {
	Path       string                `xml:"path,attr"`
	Properties []fingerprintProperty `xml:"property"`
}

// update/cleanup 不遍历无关的正常文件；全局状态负责发现新增本地修改。
func (s *Server) requestFingerprint(ctx context.Context, p Project, request domain.ActionRequest) (string, error) {
	targets := request.Targets
	if request.Action == domain.ActionUpdate || request.Action == domain.ActionCleanup {
		entries, err := s.opts.Client.Status(ctx, p.Path, false, true)
		if err != nil {
			return "", err
		}
		targets = make([]string, 0)
		for _, entry := range entries {
			if !entry.IsVersionedDirty() && !entry.IsConflict() {
				continue
			}
			if slices.ContainsFunc(request.Targets, func(parent string) bool {
				relative, err := filepath.Rel(parent, entry.Path)
				return err == nil && filepath.IsLocal(relative)
			}) {
				targets = append(targets, entry.Path)
			}
		}
	}
	return s.fingerprint(ctx, p, targets)
}

// 状态、完整属性与目标文件字节共同形成指纹，二进制变化同样使预览失效。
func (s *Server) fingerprint(ctx context.Context, p Project, targets []string) (string, error) {
	h := sha256.New()
	info, err := s.opts.Client.Info(ctx, p.Path)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	_, _ = h.Write(encoded)
	scopes := fingerprintScopes(targets)
	// 先记录全局变更调度，再记录明确目标的基础修订，避免遍历无关的正常文件。
	states := make(map[string]string)
	for _, scope := range [][]string{{"."}, scopes} {
		if len(scope) == 0 {
			continue
		}
		args := []string{"status", "--xml", "--no-ignore", "--ignore-externals"}
		if len(scope) != 1 || scope[0] != "." {
			args = append(args, "--verbose")
		}
		args = append(args, "--")
		for _, target := range scope {
			args = append(args, svn.LiteralTarget(target))
		}
		result, err := s.opts.Client.Run(ctx, p.Path, args...)
		if err != nil {
			return "", err
		}
		statuses, err := svn.ParseStatus([]byte(result.Stdout))
		if err != nil {
			return "", err
		}
		for _, entry := range statuses {
			states[filepath.Clean(entry.Path)] = entry.Item
		}
		// SVN XML 属性顺序并不固定，解析后排序才能产生稳定指纹。
		slices.SortFunc(statuses, func(a, b svn.StatusEntry) int { return strings.Compare(a.Path, b.Path) })
		encoded, err = json.Marshal(statuses)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(encoded)
	}
	for _, target := range scopes {
		// 未版本化、已删除与尚不存在的移动目标没有可读取的当前属性。
		if slices.Contains([]string{"deleted", "unversioned", "ignored", "external"}, states[target]) {
			continue
		}
		targetInfo, err := s.opts.Client.Info(ctx, filepath.Join(p.Path, target))
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		encoded, err = json.Marshal(targetInfo)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(encoded)
		result, err := s.opts.Client.Run(ctx, p.Path, "proplist", "--xml", "--verbose", "--recursive", "--", svn.LiteralTarget(target))
		if err != nil {
			return "", err
		}
		var properties struct {
			Targets []fingerprintProperties `xml:"target"`
		}
		if err := xml.Unmarshal([]byte(result.Stdout), &properties); err != nil {
			return "", err
		}
		slices.SortFunc(properties.Targets, func(a, b fingerprintProperties) int { return strings.Compare(a.Path, b.Path) })
		for i := range properties.Targets {
			slices.SortFunc(properties.Targets[i].Properties, func(a, b fingerprintProperty) int { return strings.Compare(a.Name, b.Name) })
		}
		encoded, err = json.Marshal(properties)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(encoded)
	}
	root, err := os.OpenRoot(p.Path)
	if err != nil {
		return "", err
	}
	defer root.Close()
	count := 0
	var size int64
	for _, target := range scopes {
		fmt.Fprintf(h, "\nscope:%q\n", target)
		err = filepath.WalkDir(filepath.Join(p.Path, target), func(path string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				fmt.Fprintf(h, "missing:%q", path)
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if strings.EqualFold(entry.Name(), ".svn") && entry.IsDir() {
				return filepath.SkipDir
			}
			relative, err := filepath.Rel(p.Path, path)
			if err != nil {
				return err
			}
			if relative == "." {
				return nil
			}
			count++
			if count > 100000 {
				return problem(413, "fingerprint_limit", "操作范围超过 100000 个路径，请缩小选择范围")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "\n%q:%s:%d\n", filepath.ToSlash(relative), info.Mode().String(), info.Size())
			if info.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				fmt.Fprintf(h, "%q", link)
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return problem(400, "unsupported_file", "操作范围含不支持的特殊文件: "+relative)
			}
			size += info.Size()
			if size > 2<<30 {
				return problem(413, "fingerprint_limit", "操作范围内容超过 2 GiB，无法安全生成预览")
			}
			file, err := root.Open(relative)
			if err != nil {
				return err
			}
			copied, copyErr := io.Copy(h, &contextReader{ctx: ctx, reader: io.LimitReader(file, 2<<30+1)})
			size += copied - info.Size()
			if size > 2<<30 {
				copyErr = errors.Join(copyErr, problem(413, "fingerprint_limit", "读取期间操作范围内容超过 2 GiB"))
			}
			return errors.Join(copyErr, file.Close())
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func fingerprintScopes(targets []string) []string {
	if targets == nil {
		return []string{"."}
	}
	sorted := slices.Clone(targets)
	slices.Sort(sorted)
	result := make([]string, 0, len(sorted))
	for _, target := range sorted {
		if slices.ContainsFunc(result, func(parent string) bool {
			relative, err := filepath.Rel(parent, target)
			return err == nil && filepath.IsLocal(relative)
		}) {
			continue
		}
		result = append(result, target)
	}
	return result
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
