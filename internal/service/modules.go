package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"svntui/internal/automation"
	"svntui/internal/config"
	"svntui/internal/domain"
	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/svn"
)

// Modules 为非 Workspace 页面组装只读数据。
type Modules struct {
	Client      svn.Client
	Config      config.Config
	Automations automation.Store
	SafePoints  *safepoint.Manager
	Operations  operation.Recorder
	ConfigStore config.Store
}

func (m Modules) LoadModule(ctx context.Context, request domain.ModuleRequest) (domain.ModuleData, error) {
	switch request.Module {
	case domain.ModuleRepository:
		return m.repository(ctx, request.WorkingCopy, request.Target)
	case domain.ModuleHistory:
		return m.history(ctx, request)
	case domain.ModuleBranches:
		return m.branches(), nil
	case domain.ModuleConflicts:
		return m.conflicts(ctx, request.WorkingCopy)
	case domain.ModuleAutomations:
		return m.automations()
	case domain.ModuleSafePoints:
		return m.safePoints()
	case domain.ModuleProjects:
		return m.projects(ctx)
	case domain.ModuleOperations:
		return m.operations()
	default:
		return domain.ModuleData{}, fmt.Errorf("不支持的页面: %s", request.Module)
	}
}

func (m Modules) repository(ctx context.Context, workingCopy, target string) (domain.ModuleData, error) {
	info, err := m.Client.Info(ctx, workingCopy)
	if err != nil {
		return domain.ModuleData{}, err
	}
	if target == "" {
		target = info.URL + "@"
	}
	// 浏览器生成的 URL 显式带空 peg，避免目录名末尾 @ 被 SVN 吞掉。
	location := strings.TrimSuffix(target, "@")
	entries, err := m.Client.List(ctx, target, "HEAD", "immediates")
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(entries))
	for _, entry := range entries {
		entryURL := strings.TrimRight(location, "/") + "/" + url.PathEscape(entry.Name)
		rows = append(rows, domain.ModuleRow{ID: entryURL, Target: entryURL + "@", Kind: entry.Kind, Cells: []string{entry.Kind, entry.Name, fmt.Sprintf("r%d · %s", entry.Revision, entry.Author)}, Detail: fmt.Sprintf("URL: %s\n类型: %s\nRevision: %d\n作者: %s\n时间: %s\n大小: %d", entryURL, entry.Kind, entry.Revision, entry.Author, localTime(entry.Date), entry.Size)})
	}
	parent := ""
	if strings.TrimRight(location, "/") != strings.TrimRight(info.RootURL, "/") {
		if i := strings.LastIndex(strings.TrimRight(location, "/"), "/"); i > strings.Index(location, "://")+2 {
			parent = location[:i] + "@"
		}
	}
	return domain.ModuleData{Title: "仓库: " + location, Columns: []string{"类型", "名称", "修订"}, Rows: rows, Location: target, Parent: parent, EmptyMessage: "空目录"}, nil
}

func (m Modules) history(ctx context.Context, request domain.ModuleRequest) (domain.ModuleData, error) {
	historyPath, err := workingCopyTarget(request.WorkingCopy, request.Target)
	if err != nil {
		return domain.ModuleData{}, err
	}
	since, err := ParseHistoryDate(request.Since, false)
	if err != nil {
		return domain.ModuleData{}, err
	}
	until, err := ParseHistoryDate(request.Until, true)
	if err != nil {
		return domain.ModuleData{}, err
	}
	if !since.IsZero() && !until.IsZero() && since.After(until) {
		return domain.ModuleData{}, fmt.Errorf("开始时间不能晚于结束时间")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	page, err := m.Client.LogWithOptions(ctx, historyPath, svn.LogOptions{StartRevision: request.BeforeRevision, Limit: limit, Author: request.Author, Search: request.Search, Since: since, Until: until})
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(page.Entries))
	for _, entry := range page.Entries {
		var changed strings.Builder
		for _, path := range entry.Paths {
			fmt.Fprintf(&changed, "%s %s", path.Action, path.Path)
			if path.CopyFromPath != "" {
				fmt.Fprintf(&changed, "（复制自 %s@%d）", path.CopyFromPath, path.CopyFromRev)
			}
			changed.WriteByte('\n')
		}
		rows = append(rows, domain.ModuleRow{ID: strconv.FormatInt(entry.Revision, 10), Cells: []string{fmt.Sprintf("r%d", entry.Revision), entry.Author, localTime(entry.Date), oneLine(entry.Message)}, Detail: fmt.Sprintf("Revision: r%d\n作者: %s\n时间: %s\n\n%s\n\n变化路径:\n%s", entry.Revision, entry.Author, localTime(entry.Date), entry.Message, changed.String())})
	}
	return domain.ModuleData{Title: "历史: " + historyPath, Columns: []string{"Revision", "作者", "时间", "提交说明"}, Rows: rows, NextRevision: page.NextRevision, HasMore: page.HasMore, EmptyMessage: "本页没有匹配修订，可继续下一页"}, nil
}

func ParseHistoryDate(value string, endOfDay bool) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("无效日期 %q，使用 YYYY-MM-DD 或 RFC3339", value)
	}
	if endOfDay {
		t = t.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	return t, nil
}

// LoadRevisionDiff 按需读取某次修订的实际内容差异。
func (m Modules) LoadRevisionDiff(ctx context.Context, workingCopy, target, revision string) (string, error) {
	revision = strings.TrimPrefix(strings.TrimSpace(revision), "r")
	value, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || value <= 0 {
		return "", fmt.Errorf("无效的 revision: %q", revision)
	}
	if _, err := workingCopyTarget(workingCopy, target); err != nil {
		return "", err
	}
	return m.Client.RevisionDiff(ctx, workingCopy, target, value)
}

func (m Modules) LoadMergeInfo(ctx context.Context, workingCopy, source string) (domain.MergeInfoData, error) {
	eligible, err := m.Client.MergeInfo(ctx, workingCopy, source, "eligible")
	if err != nil {
		return domain.MergeInfoData{}, err
	}
	merged, err := m.Client.MergeInfo(ctx, workingCopy, source, "merged")
	return domain.MergeInfoData{Eligible: eligible, Merged: merged}, err
}

func (m Modules) LoadBlame(ctx context.Context, workingCopy, target, revision string) (string, error) {
	path, err := workingCopyTarget(workingCopy, target)
	if err != nil {
		return "", err
	}
	lines, err := m.Client.BlameWithContent(ctx, path, revision)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&output, "%6d  r%-8d %-16s %s\n", line.Line, line.Revision, line.Author, line.Content)
	}
	return output.String(), nil
}

func (m Modules) CompareRevisions(ctx context.Context, workingCopy, target, from, to string) (string, error) {
	if _, err := workingCopyTarget(workingCopy, target); err != nil {
		return "", err
	}
	return m.Client.DiffRevisions(ctx, workingCopy, target, from, to)
}

func workingCopyTarget(workingCopy, target string) (string, error) {
	if target == "" {
		return workingCopy, nil
	}
	if strings.Contains(target, "://") {
		return target, nil
	}
	root, err := filepath.Abs(workingCopy)
	if err != nil {
		return "", fmt.Errorf("解析工作副本路径: %w", err)
	}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析历史目标: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("历史目标位于工作副本之外: %s", target)
	}
	return path, nil
}

func (m Modules) branches() domain.ModuleData {
	rows := make([]domain.ModuleRow, 0, len(m.Config.Favorites))
	for _, favorite := range m.Config.Favorites {
		rows = append(rows, domain.ModuleRow{
			ID: favorite.URL, Cells: []string{favorite.Group, favorite.Name, favorite.URL},
			Detail: fmt.Sprintf("分组: %s\n名称: %s\nURL: %s\n\n使用 :copy、:switch 或 :merge 操作此路径。", favorite.Group, favorite.Name, favorite.URL),
		})
	}
	return domain.ModuleData{Title: "分支路径收藏", Columns: []string{"分组", "名称", "URL"}, Rows: rows, EmptyMessage: "尚未收藏路径；使用 favorite add 命令添加"}
}

func (m Modules) conflicts(ctx context.Context, workingCopy string) (domain.ModuleData, error) {
	entries, err := m.Client.Status(ctx, workingCopy, false, true)
	if err != nil {
		return domain.ModuleData{}, err
	}
	var rows []domain.ModuleRow
	for _, entry := range entries {
		if !entry.IsConflict() {
			continue
		}
		kind := "文本"
		switch {
		case entry.TreeConflicted:
			kind = "树"
		case entry.Properties == "conflicted":
			kind = "属性"
		}
		rows = append(rows, domain.ModuleRow{
			ID: entry.Path, Cells: []string{entry.Path, kind, entry.Item},
			Detail: conflictDetail(workingCopy, entry, kind),
		})
	}
	return domain.ModuleData{Title: "冲突", Columns: []string{"路径", "类型", "状态"}, Rows: rows, EmptyMessage: "当前没有未解决冲突"}, nil
}

func conflictDetail(workingCopy string, entry svn.StatusEntry, kind string) string {
	var detail strings.Builder
	fmt.Fprintf(&detail, "路径: %s\n类型: %s\n状态: %s\n", entry.Path, kind, entry.Item)
	if entry.TreeConflicted {
		fmt.Fprintf(&detail, "\n这是树冲突。操作: %s\n本地/旧侧: %s %s@r%d\n远端/新侧: %s %s@r%d\n", entry.ConflictOperation, entry.ConflictLeft.Kind, entry.ConflictLeft.Path, entry.ConflictLeft.Revision, entry.ConflictRight.Kind, entry.ConflictRight.Path, entry.ConflictRight.Revision)
		detail.WriteString("请根据两侧路径和 revision 判断新增、删除或移动关系，再选择完整版本或先在外部工具中整理目录。\n")
	}
	artifacts := []struct {
		name string
		path string
	}{
		{"BASE", entry.ConflictOld},
		{"MINE", entry.ConflictNew},
		{"THEIRS", entry.ConflictWorking},
	}
	for _, artifact := range artifacts {
		if artifact.path == "" {
			continue
		}
		fmt.Fprintf(&detail, "\n===== %s: %s =====\n%s\n", artifact.name, artifact.path, readConflictArtifact(workingCopy, artifact.path))
	}
	detail.WriteString("\n使用 :resolve working|mine-full|theirs-full|base 处理完整版本；逐块编辑后使用 :resolve working 标记。")
	return detail.String()
}

func readConflictArtifact(workingCopy, artifact string) string {
	root, err := filepath.Abs(workingCopy)
	if err != nil {
		return "[无法解析工作副本路径]"
	}
	path := artifact
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "[无法解析冲突文件路径]"
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "[冲突文件位于工作副本之外，已拒绝读取]"
	}
	file, err := os.Open(path)
	if err != nil {
		return "[无法读取: " + err.Error() + "]"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 256*1024+1))
	if err != nil {
		return "[读取失败: " + err.Error() + "]"
	}
	if len(data) > 256*1024 {
		return "[文件超过 256 KiB，请使用外部编辑器]"
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "[二进制内容，请使用外部合并工具]"
	}
	return string(data)
}

func (m Modules) automations() (domain.ModuleData, error) {
	tasks, err := m.Automations.Tasks()
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, domain.ModuleRow{
			ID: task.ID, Cells: []string{task.Name, string(task.Status), nextRun(task.NextRunAt), task.WorkingCopy},
			Detail: fmt.Sprintf("ID: %s\n分支: %s\n间隔: %s\n状态: %s\n上次结果: %s/%s\n暂停原因: %s", task.ID, task.BranchURL, task.Interval.Duration(), task.Status, task.LastResult, task.LastOutcome, task.PauseReason),
		})
	}
	return domain.ModuleData{Title: "自动更新任务", Columns: []string{"名称", "状态", "下次运行", "工作副本"}, Rows: rows, EmptyMessage: "尚未配置自动更新任务"}, nil
}

func (m Modules) safePoints() (domain.ModuleData, error) {
	if m.SafePoints == nil {
		return domain.ModuleData{Title: "安全快照", EmptyMessage: "SafePoint 服务不可用"}, nil
	}
	manifests, err := m.SafePoints.List()
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(manifests))
	for _, manifest := range manifests {
		rows = append(rows, domain.ModuleRow{
			ID: manifest.ID, Cells: []string{localTime(manifest.CreatedAt), manifest.Reason, byteSize(manifest.SizeBytes), manifest.WorkingCopy},
			Detail: formatSafePoint(manifest),
		})
	}
	return domain.ModuleData{Title: "安全快照", Columns: []string{"创建时间", "原因", "大小", "工作副本"}, Rows: rows, EmptyMessage: "尚无 SafePoint"}, nil
}

func (m Modules) operations() (domain.ModuleData, error) {
	records, err := m.Operations.Last(100)
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(records))
	for _, record := range records {
		status := "成功"
		if record.ExitCode != 0 || record.ErrorKind != "" {
			status = string(record.ErrorKind)
		}
		rows = append(rows, domain.ModuleRow{
			ID: record.ID, Cells: []string{localTime(record.StartedAt), strings.Join(record.Arguments, " "), status, record.Duration.Round(time.Millisecond).String()},
			Detail: fmt.Sprintf("目录: %s\n命令: svn %s\n退出码: %d\n错误: %s", record.Directory, strings.Join(record.Arguments, " "), record.ExitCode, record.ErrorBrief),
		})
	}
	return domain.ModuleData{Title: "最近 100 次 SVN 操作", Columns: []string{"时间", "命令", "结果", "耗时"}, Rows: rows, EmptyMessage: "尚无操作记录"}, nil
}

func formatInfo(info svn.Info) string {
	return fmt.Sprintf("URL: %s\n仓库根 URL: %s\nUUID: %s\nRevision: %d\n工作副本: %s\n检出深度: %s", info.URL, info.RootURL, info.UUID, info.Revision, info.WCAbsolute, info.Depth)
}

func formatSafePoint(manifest safepoint.Manifest) string {
	var entries strings.Builder
	for _, entry := range manifest.Entries {
		fmt.Fprintf(&entries, "%s %s", entry.Status, entry.Path)
		if entry.Binary {
			entries.WriteString(" [二进制]")
		}
		if entry.Unversioned {
			entries.WriteString(" [未版本化]")
		}
		entries.WriteByte('\n')
	}
	return fmt.Sprintf("ID: %s\n创建时间: %s\n原因: %s\n仓库 UUID: %s\nURL: %s\n基准 revision: %d\n大小: %s\n\n条目:\n%s", manifest.ID, localTime(manifest.CreatedAt), manifest.Reason, manifest.RepositoryUUID, manifest.URL, manifest.BaseRevision, byteSize(manifest.SizeBytes), entries.String())
}

func nextRun(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return localTime(value)
}

func localTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func byteSize(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	if value < unit*unit {
		return fmt.Sprintf("%.1f KiB", float64(value)/unit)
	}
	if value < unit*unit*unit {
		return fmt.Sprintf("%.1f MiB", float64(value)/(unit*unit))
	}
	return fmt.Sprintf("%.1f GiB", float64(value)/(unit*unit*unit))
}
