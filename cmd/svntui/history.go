package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"svntui/internal/service"
	"svntui/internal/svn"
)

func runHistory(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage("history 子命令: log, blame, compare")
	}
	mode := args[0]
	flags := newFlagSet("history "+mode, stderr)
	revision := flags.String("revision", "", "修订或范围")
	path := flags.String("path", ".", "默认工作副本路径")
	limit := flags.Int("limit", 100, "返回数量（1-1000）")
	before := flags.Int64("before", 0, "从此修订开始向前查询（包含此修订）")
	author := flags.String("author", "", "按作者过滤")
	search := flags.String("search", "", "搜索提交说明及变更路径")
	since := flags.String("since", "", "开始日期 YYYY-MM-DD/RFC3339")
	until := flags.String("until", "", "结束日期 YYYY-MM-DD/RFC3339")
	pageOnly := flags.Bool("page", false, "返回单页及下一页游标")
	left := flags.String("left", "", "对比起始修订")
	right := flags.String("right", "", "对比结束修订")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return usage("history 只接受一个目标文件、目录或 URL")
	}
	target := *path
	if flags.NArg() == 1 {
		target = flags.Arg(0)
		if !filepath.IsAbs(target) && !strings.Contains(target, "://") {
			target = filepath.Join(*path, target)
		}
	}
	switch mode {
	case "log":
		if *limit < 1 || *limit > 1000 || *before < 0 {
			return usage("limit 必须为 1-1000，before 不能为负数")
		}
		from, err := service.ParseHistoryDate(*since, false)
		if err != nil {
			return usage(err.Error())
		}
		to, err := service.ParseHistoryDate(*until, true)
		if err != nil {
			return usage(err.Error())
		}
		if !from.IsZero() && !to.IsZero() && from.After(to) {
			return usage("开始时间不能晚于结束时间")
		}
		opts := svn.LogOptions{RevisionRange: *revision, StartRevision: *before, Limit: *limit, Author: *author, Search: *search, Since: from, Until: to}
		page, err := env.client.LogWithOptions(ctx, target, opts)
		if err != nil {
			return err
		}
		if *pageOnly {
			return writeJSON(stdout, page)
		}
		entries := page.Entries
		// 过滤后不足一页时继续扫描，避免把无匹配的一页误认为历史结束。
		for len(entries) < *limit && page.HasMore {
			opts.StartRevision = page.NextRevision
			opts.Limit = *limit - len(entries)
			page, err = env.client.LogWithOptions(ctx, target, opts)
			if err != nil {
				return err
			}
			entries = append(entries, page.Entries...)
		}
		return writeJSON(stdout, entries)
	case "blame":
		lines, err := env.client.BlameWithContent(ctx, target, *revision)
		if err != nil {
			return err
		}
		return writeJSON(stdout, lines)
	case "compare":
		if *left == "" || *right == "" {
			return usage("compare 需要 --left 和 --right")
		}
		localTarget := "."
		if flags.NArg() == 1 {
			localTarget = flags.Arg(0)
		}
		diff, err := env.client.DiffRevisions(ctx, *path, localTarget, *left, *right)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(stdout, diff)
		return err
	default:
		return usage("history 子命令: log, blame, compare")
	}
}
