package svn

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// 游标基于扫描到的修订，筛选为空的页仍可以继续翻页。
func (c Client) LogWithOptions(ctx context.Context, path string, opts LogOptions) (LogPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	revisionRange, lower, err := c.historyRange(ctx, path, opts)
	if err != nil {
		return LogPage{}, err
	}
	if opts.StartRevision > 0 && opts.StartRevision < lower {
		return LogPage{}, nil
	}
	scanned, err := c.Log(ctx, path, revisionRange, limit)
	if err != nil {
		return LogPage{}, err
	}
	page := LogPage{}
	if len(scanned) == 0 {
		return page, nil
	}
	lastRevision := scanned[len(scanned)-1].Revision
	page.NextRevision = lastRevision - 1
	page.HasMore = len(scanned) == limit && lastRevision > max(lower, 1)
	// 先分页后筛选，避免服务端筛选造成游标跳跃或停滞。
	for _, entry := range scanned {
		if opts.Author != "" && !strings.EqualFold(entry.Author, opts.Author) {
			continue
		}
		if !opts.Since.IsZero() && entry.Date.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && entry.Date.After(opts.Until) {
			continue
		}
		if opts.Search != "" && !matchesLogSearch(entry, opts.Search) {
			continue
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func (c Client) historyRange(ctx context.Context, path string, opts LogOptions) (string, int64, error) {
	start, end := "HEAD", "0"
	lower := int64(0)
	if opts.RevisionRange != "" {
		first, last := splitHistoryRange(opts.RevisionRange)
		resolve := func(value string) (int64, error) {
			value = strings.TrimSpace(value)
			if revision, err := strconv.ParseInt(value, 10, 64); err == nil && revision >= 0 {
				return revision, nil
			}
			// 日期和 HEAD 在仓库根解析，避免目标尚未创建导致端点查询失败。
			target := path
			if strings.EqualFold(value, "HEAD") || strings.HasPrefix(value, "{") {
				info, err := c.Info(ctx, path)
				if err != nil {
					return 0, err
				}
				target = info.RootURL
			}
			info, err := c.InfoAt(ctx, target, value)
			if err != nil {
				return 0, fmt.Errorf("解析历史修订 %q: %w", value, err)
			}
			return info.Revision, nil
		}
		left, err := resolve(first)
		if err != nil {
			return "", 0, err
		}
		right := left
		if last != first {
			right, err = resolve(last)
			if err != nil {
				return "", 0, err
			}
		}
		lower = min(left, right)
		start, end = strconv.FormatInt(max(left, right), 10), strconv.FormatInt(lower, 10)
	}
	if opts.StartRevision > 0 {
		cursor := opts.StartRevision
		if upper, err := strconv.ParseInt(start, 10, 64); err == nil {
			cursor = min(cursor, upper)
		}
		start = strconv.FormatInt(cursor, 10)
	}
	return start + ":" + end, lower, nil
}

func splitHistoryRange(value string) (string, string) {
	date := false
	for index, char := range value {
		switch char {
		case '{':
			date = true
		case '}':
			date = false
		case ':':
			if !date {
				return value[:index], value[index+1:]
			}
		}
	}
	return value, value
}

func matchesLogSearch(entry LogEntry, search string) bool {
	search = strings.ToLower(search)
	if strings.Contains(strings.ToLower(entry.Message), search) || strings.Contains(strings.ToLower(entry.Author), search) {
		return true
	}
	for _, path := range entry.Paths {
		if strings.Contains(strings.ToLower(path.Path), search) || strings.Contains(strings.ToLower(path.CopyFromPath), search) {
			return true
		}
	}
	return false
}
