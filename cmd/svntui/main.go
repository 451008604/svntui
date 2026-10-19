package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/automation"
	"svntui/internal/config"
	"svntui/internal/domain"
	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/service"
	"svntui/internal/svn"
	"svntui/internal/tui"
	"svntui/internal/wclock"
)

const version = "0.3.0"

var errHelpDisplayed = errors.New("帮助信息已显示")

type environment struct {
	dirs            appdirs.Dirs
	client          svn.Client
	configStore     config.Store
	config          config.Config
	automationStore automation.Store
	automation      automation.Runner
	safePoints      *safepoint.Manager
	operations      operation.Recorder
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, errHelpDisplayed) {
			return
		}
		fmt.Fprintln(os.Stderr, "svntui:", err)
		os.Exit(commandExitCode(err))
	}
}

func runInternal(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			client := svn.NewClient()
			fmt.Fprintf(stdout, "svntui %s (svn %s)\n", version, svnVersion(ctx, client))
			return nil
		case "help", "--help", "-h":
			printHelp(stdout)
			return nil
		}
	}
	env, err := newEnvironment()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return runTUI(ctx, env, nil)
	}
	switch args[0] {
	case "web":
		return runWeb(ctx, env, args[1:], stdout, stderr)
	case "commit", "update", "add", "delete", "move", "revert", "cleanup", "lock", "unlock", "resolve", "switch", "merge":
		return runWorkspaceAction(ctx, env, args[0], args[1:], stdout, stderr)
	case "changelist":
		return runChangelist(ctx, env, args[1:], stdout, stderr)
	case "property", "externals":
		return runProperty(ctx, env, args[0], args[1:], stdout, stderr)
	case "patch":
		return runPatch(ctx, env, args[1:], stdout, stderr)
	case "project":
		return runProject(ctx, env, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, env, args[1:], stdout, stderr)
	case "diff":
		return runDiff(ctx, env, args[1:], stdout, stderr)
	case "repository":
		return runRepository(ctx, env, args[1:], stdout, stderr)
	case "history":
		return runHistory(ctx, env, args[1:], stdout, stderr)
	case "favorite":
		return runFavorite(env, args[1:], stdout, stderr)
	case "config":
		return runConfig(env, args[1:], stdout)
	case "conflict":
		return runConflict(ctx, env, args[1:], stdout, stderr)
	case "automation":
		return runAutomation(ctx, env, args[1:], stdout, stderr)
	case "safepoint":
		return runSafePoint(ctx, env, args[1:], stdout, stderr)
	default:
		return runTUI(ctx, env, args)
	}
}

func runRepository(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("repository 子命令: list, compare, checkout, export, mkdir, delete, move, copy")
	}
	switch args[0] {
	case "list":
		flags := newFlagSet("repository list", stderr)
		revision := flags.String("revision", "HEAD", "指定 revision")
		depth := flags.String("depth", "immediates", "SVN 检出深度")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("用法: svntui repository list [--revision REV] [--depth DEPTH] URL")
		}
		entries, err := env.client.List(ctx, flags.Arg(0), *revision, *depth)
		if err != nil {
			return err
		}
		return writeJSON(stdout, entries)
	case "compare":
		flags := newFlagSet("repository compare", stderr)
		summary := flags.Bool("summary", false, "仅显示发生变化的路径")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 2 {
			return errors.New("用法: svntui repository compare [--summary] LEFT_URL RIGHT_URL")
		}
		result, err := env.client.Compare(ctx, flags.Arg(0), flags.Arg(1), *summary)
		return printSVNResult(stdout, result, err)
	case "checkout", "export":
		flags := newFlagSet("repository "+args[0], stderr)
		revision := flags.String("revision", "HEAD", "指定 revision")
		depth := flags.String("depth", "infinity", "SVN 检出深度")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 2 {
			return fmt.Errorf("用法: svntui repository %s [选项] URL TARGET", args[0])
		}
		var result svn.Result
		var err error
		if args[0] == "checkout" {
			result, err = env.client.Checkout(ctx, flags.Arg(0), flags.Arg(1), *revision, *depth)
			if err == nil {
				if info, infoErr := env.client.Info(ctx, flags.Arg(1)); infoErr == nil && info.WCAbsolute != "" {
					err = env.configStore.Update(func(cfg *config.Config) error {
						cfg.RememberProject(info.WCAbsolute)
						return nil
					})
				}
			}
		} else {
			result, err = env.client.Export(ctx, flags.Arg(0), flags.Arg(1), *revision, *depth)
		}
		return printSVNResult(stdout, result, err)
	case "mkdir", "delete", "move", "copy":
		flags := newFlagSet("repository "+args[0], stderr)
		message := flags.String("message", "", "提交说明")
		revision := flags.String("revision", "HEAD", "copy 使用的源 revision")
		yes := flags.Bool("yes", false, "确认已核对完整远端 URL")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		wantArgs := 1
		if args[0] == "move" || args[0] == "copy" {
			wantArgs = 2
		}
		if flags.NArg() != wantArgs || strings.TrimSpace(*message) == "" {
			return fmt.Errorf("repository %s 需要 %d 个 URL、--message 和 --yes", args[0], wantArgs)
		}
		if !*yes {
			displayURLs := slices.Clone(flags.Args())
			for index := range displayURLs {
				displayURLs[index] = publicURL(displayURLs[index])
			}
			return fmt.Errorf("远端写操作已阻止；核对 URL 后追加 --yes: %s", strings.Join(displayURLs, " -> "))
		}
		var result svn.Result
		var err error
		switch args[0] {
		case "mkdir":
			result, err = env.client.RemoteMkdir(ctx, flags.Arg(0), *message)
		case "delete":
			result, err = env.client.RemoteDelete(ctx, flags.Arg(0), *message)
		case "move":
			result, err = env.client.RemoteMove(ctx, flags.Arg(0), flags.Arg(1), *message)
		case "copy":
			result, err = env.client.RemoteCopy(ctx, flags.Arg(0), flags.Arg(1), *revision, *message)
		}
		return printSVNResult(stdout, result, err)
	default:
		return errors.New("repository 子命令: list, compare, checkout, export, mkdir, delete, move, copy")
	}
}

func publicURL(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
		return parsed.String()
	}
	return value
}

func printSVNResult(stdout io.Writer, result svn.Result, runErr error) error {
	if result.Stdout != "" {
		_, _ = io.WriteString(stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprintln(stdout, result.Stderr)
	}
	return runErr
}

func runConflict(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法: svntui conflict <edit|merge-tool> [--path 工作副本] <冲突文件>")
	}
	flags := newFlagSet("conflict "+args[0], stderr)
	workingCopy := flags.String("path", ".", "工作副本路径")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("必须指定一个冲突文件")
	}
	target, err := secureWorkingCopyPath(*workingCopy, flags.Arg(0))
	if err != nil {
		return err
	}
	info, err := env.client.Info(ctx, *workingCopy)
	if err != nil {
		return err
	}
	lockRoot := info.WCAbsolute
	if lockRoot == "" {
		lockRoot = *workingCopy
	}
	lock, err := wclock.Acquire(filepath.Join(env.dirs.State, "working-copy-locks"), lockRoot, "冲突处理外部工具")
	if err != nil {
		return err
	}
	defer lock.Release()
	entries, err := env.client.Status(ctx, *workingCopy, false, true)
	if err != nil {
		return err
	}
	var conflict svn.StatusEntry
	found := false
	for _, entry := range entries {
		entryPath, pathErr := secureWorkingCopyPath(*workingCopy, entry.Path)
		if pathErr == nil && entryPath == target && entry.IsConflict() {
			conflict = entry
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s 当前不是 SVN 冲突文件", flags.Arg(0))
	}

	var command *exec.Cmd
	switch args[0] {
	case "edit":
		editor := env.config.Editor
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			return errors.New("未配置编辑器；执行 svntui config set editor <可执行文件>")
		}
		command = exec.CommandContext(ctx, editor, target)
	case "merge-tool":
		if env.config.MergeTool == "" {
			return errors.New("未配置合并工具；执行 svntui config set merge-tool <可执行文件>")
		}
		base, err := secureWorkingCopyPath(*workingCopy, conflict.ConflictOld)
		if err != nil {
			return fmt.Errorf("无法定位 BASE 文件: %w", err)
		}
		mine, err := secureWorkingCopyPath(*workingCopy, conflict.ConflictNew)
		if err != nil {
			return fmt.Errorf("无法定位 MINE 文件: %w", err)
		}
		theirs, err := secureWorkingCopyPath(*workingCopy, conflict.ConflictWorking)
		if err != nil {
			return fmt.Errorf("无法定位 THEIRS 文件: %w", err)
		}
		command = exec.CommandContext(ctx, env.config.MergeTool, base, mine, theirs, target)
	default:
		return errors.New("conflict 子命令: edit, merge-tool")
	}
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("外部工具执行失败: %w", errors.Join(ctx.Err(), err))
	}
	refreshed, err := env.client.Status(ctx, *workingCopy, false, true)
	if err != nil {
		return err
	}
	stillConflicted := slices.ContainsFunc(refreshed, func(entry svn.StatusEntry) bool {
		path, pathErr := secureWorkingCopyPath(*workingCopy, entry.Path)
		return pathErr == nil && path == target && entry.IsConflict()
	})
	if stillConflicted {
		fmt.Fprintln(stdout, "文件仍处于冲突状态；复查后执行 :resolve working 或 svn resolve --accept working")
	} else {
		fmt.Fprintln(stdout, "外部工具已退出，SVN 状态已重新读取")
	}
	return nil
}

func secureWorkingCopyPath(workingCopy, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("路径不能为空")
	}
	root, err := filepath.Abs(workingCopy)
	if err != nil {
		return "", err
	}
	path := target
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径超出工作副本: %s", target)
	}
	return path, nil
}

func runConfig(env environment, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "show" {
		return writeJSON(stdout, env.config)
	}
	if len(args) != 3 || args[0] != "set" {
		return errors.New("用法: svntui config <show|set KEY VALUE>")
	}
	return env.configStore.Update(func(cfg *config.Config) error {
		key, value := args[1], args[2]
		switch key {
		case "expert-mode":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("expert-mode 必须是 true 或 false: %w", err)
			}
			cfg.ExpertMode = parsed
		case "editor":
			cfg.Editor = value
		case "merge-tool":
			cfg.MergeTool = value
		case "no-color":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("no-color 必须是 true 或 false: %w", err)
			}
			cfg.IgnoreColors = parsed
		default:
			return fmt.Errorf("未知配置项 %q；支持 expert-mode、editor、merge-tool、no-color", key)
		}
		return nil
	})
}

func newEnvironment() (environment, error) {
	dirs, err := appdirs.Resolve()
	if err != nil {
		return environment{}, err
	}
	if err := dirs.Ensure(); err != nil {
		return environment{}, err
	}
	configStore := config.NewStore(dirs)
	cfg, err := configStore.Load()
	if err != nil {
		return environment{}, err
	}
	recorder := operation.Recorder{Path: filepath.Join(dirs.State, "operations.jsonl")}
	client := svn.NewClient()
	client.Observer = recorder.Observe
	automationStore := automation.NewStore(dirs)
	runner := automation.NewRunner(automationStore, client)
	return environment{
		dirs:            dirs,
		client:          client,
		configStore:     configStore,
		config:          cfg,
		automationStore: automationStore,
		automation:      runner,
		safePoints:      safepoint.New(dirs, client),
		operations:      recorder,
	}, nil
}

func runTUI(ctx context.Context, env environment, args []string) error {
	if wantsJSON(ctx) {
		return usage("交互界面不支持 --json；请使用 status、project list 等脚本命令")
	}
	flags := newFlagSet("", io.Discard)
	noColor := flags.Bool("no-color", env.config.IgnoreColors, "禁用 ANSI 颜色")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("用法: svntui [--no-color] [工作副本路径]")
	}
	path := ""
	if flags.NArg() == 1 {
		path = flags.Arg(0)
	} else {
		path, _ = os.Getwd()
	}
	workingCopy, err := resolveWorkingCopy(ctx, env.client, path, flags.NArg() == 0, env.config.Recent)
	if err != nil {
		return err
	}
	if err := env.configStore.Update(func(cfg *config.Config) error { cfg.RememberProject(workingCopy); return nil }); err != nil {
		return err
	}

	lockDir := filepath.Join(env.dirs.State, "working-copy-locks")
	actions := service.Actions{
		Client: env.client, SafePoints: env.safePoints, LockDir: lockDir, ExpertMode: env.config.ExpertMode,
	}
	modules := service.Modules{
		Client: env.client, Config: env.config, Automations: env.automationStore,
		SafePoints: env.safePoints, Operations: env.operations, ConfigStore: env.configStore,
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	schedulerDone := make(chan error, 1)
	go func() {
		schedulerDone <- automation.NewScheduler(env.automationStore, env.automation).Run(sessionCtx)
	}()
	err = tui.Run(sessionCtx, tui.Options{
		WorkingCopy: workingCopy, Actions: actions, ModuleLoader: modules, NoColor: *noColor, Editor: env.config.Editor,
		RunExternal: func(ctx context.Context, request domain.ExternalRequest) error {
			return runExternal(ctx, env, request, os.Stdout, os.Stderr)
		},
	})
	cancel()
	select {
	case schedulerErr := <-schedulerDone:
		if schedulerErr != nil && !errors.Is(schedulerErr, context.Canceled) {
			return errors.Join(err, schedulerErr)
		}
	case <-time.After(time.Second):
	}
	return err
}

func resolveWorkingCopy(ctx context.Context, client svn.Client, path string, tryRecent bool, recent []string) (string, error) {
	info, err := client.Info(ctx, path)
	if err == nil {
		if info.WCAbsolute != "" {
			return info.WCAbsolute, nil
		}
		absolute, absErr := filepath.Abs(path)
		return absolute, absErr
	}
	if tryRecent {
		for _, candidate := range recent {
			info, candidateErr := client.Info(ctx, candidate)
			if candidateErr == nil {
				if info.WCAbsolute != "" {
					return info.WCAbsolute, nil
				}
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("%s 不是 SVN 工作副本；请传入工作副本路径或先执行 svn checkout: %w", path, err)
}

func runStatus(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("status", stderr)
	remote := flags.Bool("remote", false, "同时查询远端状态")
	pathFlag := flags.String("path", ".", "工作副本路径")
	group := flags.String("changelist", "", "仅显示此变更分组")
	ignored := flags.Bool("ignored", true, "包含 ignored 路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	path := *pathFlag
	if flags.NArg() == 1 {
		path = flags.Arg(0)
	} else if flags.NArg() > 1 {
		return errors.New("用法: svntui status [--remote] [路径]")
	}
	entries, err := env.client.Status(ctx, path, *remote, *ignored)
	if err != nil {
		return err
	}
	if *group != "" {
		entries = slices.DeleteFunc(entries, func(entry svn.StatusEntry) bool { return entry.Changelist != *group })
	}
	if wantsJSON(ctx) {
		return writeJSON(stdout, entries)
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "状态\t属性\t远端内容\t远端属性\t分组\tREVISION\t路径")
	for _, entry := range entries {
		flags := ""
		if entry.Switched {
			flags += "S"
		}
		if entry.TreeConflicted {
			flags += "C"
		}
		if flags != "" {
			flags = "/" + flags
		}
		fmt.Fprintf(writer, "%s%s\t%s\t%s\t%s\t%s\t%d\t%s\n", entry.Item, flags, entry.Properties, entry.RepositoryItem, entry.RepositoryProps, entry.Changelist, entry.Revision, entry.Path)
	}
	return writer.Flush()
}

func runDiff(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("diff", stderr)
	workingCopy := flags.String("working-copy", ".", "工作副本路径")
	flags.StringVar(workingCopy, "path", ".", "工作副本路径（与 --working-copy 等效）")
	if err := flags.Parse(args); err != nil {
		return err
	}
	diff, err := (service.Actions{Client: env.client}).WorkspaceDiff(ctx, *workingCopy, flags.Args())
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, diff)
	return err
}

func runFavorite(env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "分组\t名称\tURL")
		for _, favorite := range env.config.Favorites {
			fmt.Fprintf(writer, "%s\t%s\t%s\n", favorite.Group, favorite.Name, favorite.URL)
		}
		return writer.Flush()
	}
	switch args[0] {
	case "add":
		flags := newFlagSet("favorite add", stderr)
		name := flags.String("name", "", "显示名称")
		group := flags.String("group", "", "分组名称")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 || strings.TrimSpace(*name) == "" {
			return errors.New("用法: svntui favorite add --name 名称 [--group 分组] URL")
		}
		// 拒绝把 URL 内嵌密码写入收藏配置，也不在错误中回显原 URL。
		target, err := url.Parse(flags.Arg(0))
		if err != nil {
			return errors.New("收藏 URL 格式无效")
		}
		if target.User != nil {
			if _, hasPassword := target.User.Password(); hasPassword {
				return errors.New("收藏 URL 不能包含密码，请使用系统 SVN 认证配置")
			}
		}
		return env.configStore.Update(func(cfg *config.Config) error {
			cfg.AddFavorite(config.Favorite{Name: *name, Group: *group, URL: flags.Arg(0)})
			return nil
		})
	case "remove":
		if len(args) != 2 {
			return errors.New("用法: svntui favorite remove <名称或URL>")
		}
		return env.configStore.Update(func(cfg *config.Config) error {
			before := len(cfg.Favorites)
			cfg.Favorites = slices.DeleteFunc(cfg.Favorites, func(item config.Favorite) bool {
				return item.Name == args[1] || item.URL == args[1]
			})
			if len(cfg.Favorites) == before {
				return errors.New("没有匹配的收藏路径")
			}
			return nil
		})
	default:
		return errors.New("favorite 子命令: list, add, remove")
	}
}

func runAutomation(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		return listAutomations(env, stdout)
	}
	switch args[0] {
	case "add":
		return addAutomation(ctx, env, args[1:], stdout, stderr)
	case "delete":
		if len(args) != 2 {
			return errors.New("用法: svntui automation delete <task-id>")
		}
		return env.automationStore.DeleteTask(args[1])
	case "run":
		flags := newFlagSet("automation run", stderr)
		once := flags.Bool("once", false, "仅执行一次到期任务")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		scheduler := automation.NewScheduler(env.automationStore, env.automation)
		if *once {
			return scheduler.RunDue(ctx, time.Now())
		}
		fmt.Fprintln(stdout, "svntui 自动更新调度器正在运行")
		return scheduler.Run(ctx)
	case "run-now":
		if len(args) != 2 {
			return errors.New("用法: svntui automation run-now <task-id>")
		}
		record, err := env.automation.RunTask(ctx, args[1], automation.TriggerManual)
		if encodeErr := writeJSON(stdout, record); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	case "pause":
		if len(args) < 2 {
			return errors.New("用法: svntui automation pause <task-id> [原因]")
		}
		return env.automation.PauseTask(ctx, args[1], strings.Join(args[2:], " "))
	case "disable":
		if len(args) != 2 {
			return errors.New("用法: svntui automation disable <task-id>")
		}
		task, err := env.automationStore.Task(args[1])
		if err != nil {
			return err
		}
		task.Enabled = false
		task.Status = automation.StatusDisabled
		task.NextRunAt = time.Time{}
		_, err = env.automationStore.UpdateTask(task)
		return err
	case "resume":
		if len(args) != 2 {
			return errors.New("用法: svntui automation resume <task-id>")
		}
		return env.automation.ResumeTask(ctx, args[1])
	case "runs":
		if len(args) != 2 {
			return errors.New("用法: svntui automation runs <task-id>")
		}
		runs, err := env.automationStore.Runs(args[1], 50)
		if err != nil {
			return err
		}
		return writeJSON(stdout, runs)
	case "webhook":
		return runWebhook(ctx, env, args[1:], stdout, stderr)
	case "service":
		return runService(ctx, env, args[1:], stdout)
	default:
		return errors.New("automation 子命令: list, add, delete, run, run-now, pause, resume, disable, runs, webhook, service")
	}
}

func listAutomations(env environment, stdout io.Writer) error {
	tasks, err := env.automationStore.Tasks()
	if err != nil {
		return err
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\t状态\t间隔\t下次运行\t名称\t工作副本")
	for _, task := range tasks {
		next := "-"
		if !task.NextRunAt.IsZero() {
			next = task.NextRunAt.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", task.ID, task.Status, task.Interval.Duration(), next, task.Name, task.WorkingCopy)
	}
	return writer.Flush()
}

func addAutomation(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("automation add", stderr)
	name := flags.String("name", "", "任务名称")
	path := flags.String("path", ".", "工作副本路径")
	interval := flags.Duration("interval", 15*time.Minute, "更新间隔")
	days := flags.String("days", "", "以逗号分隔的星期缩写")
	windows := flags.String("windows", "", "以逗号分隔的 HH:MM-HH:MM 活跃时间段")
	timeZone := flags.String("timezone", "", "IANA 时区；默认为本地时区")
	webhookID := flags.String("webhook", "", "webhook ID")
	notifySuccess := flags.Bool("notify-success", false, "有远端变化且更新成功时通知")
	notifyNoChange := flags.Bool("notify-no-change", false, "没有远端变化时通知")
	updateExternals := flags.Bool("update-externals", false, "更新 SVN externals")
	allowSwitched := flags.Bool("allow-switched", false, "允许存在 switched 子树")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" || *interval <= 0 {
		return errors.New("--name 必填且 --interval 必须大于零")
	}
	task, err := automation.NewTask(ctx, env.client, *name, *path)
	if err != nil {
		return err
	}
	task.Interval = automation.Duration(*interval)
	task.TimeZone = *timeZone
	task.Schedule, err = parseSchedule(*days, *windows)
	if err != nil {
		return err
	}
	if *webhookID != "" {
		if _, err := env.automationStore.Webhook(*webhookID); err != nil {
			return err
		}
	}
	task.WebhookID = *webhookID
	task.Notifications = automation.NotificationPolicy{Succeeded: *notifySuccess, NoChange: *notifyNoChange}
	task.UpdateExternals = *updateExternals
	task.AllowSwitched = *allowSwitched
	nextCandidate := time.Now().Add(*interval)
	if *timeZone != "" {
		location, locationErr := time.LoadLocation(*timeZone)
		if locationErr != nil {
			return fmt.Errorf("无效时区 %q: %w", *timeZone, locationErr)
		}
		nextCandidate = nextCandidate.In(location)
	}
	task.NextRunAt = task.Schedule.NextAllowed(nextCandidate)
	task, err = env.automationStore.UpsertTask(task)
	if err != nil {
		return err
	}
	return writeJSON(stdout, task)
}

func runWebhook(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		state, err := env.automationStore.Load()
		if err != nil {
			return err
		}
		webhooks := make([]automation.WebhookConfig, len(state.Webhooks))
		for index, webhook := range state.Webhooks {
			webhooks[index] = publicWebhook(webhook)
		}
		return writeJSON(stdout, webhooks)
	}
	switch args[0] {
	case "add":
		flags := newFlagSet("automation webhook add", stderr)
		name := flags.String("name", "", "webhook 名称")
		url := flags.String("url", "", "HTTP 接收地址")
		secretRef := flags.String("secret-ref", "", "env:NAME 或 file:/path")
		var headers stringList
		flags.Var(&headers, "header", "静态请求头 KEY=VALUE（可重复指定）")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		parsedHeaders, err := parseHeaders(headers)
		if err != nil {
			return err
		}
		webhook, err := env.automationStore.UpsertWebhook(automation.WebhookConfig{Name: *name, URL: *url, SecretRef: *secretRef, Headers: parsedHeaders})
		if err != nil {
			return err
		}
		return writeJSON(stdout, publicWebhook(webhook))
	case "test":
		if len(args) != 2 {
			return errors.New("用法: svntui automation webhook test <webhook-id>")
		}
		delivery := env.automation.TestWebhook(ctx, args[1])
		if err := writeJSON(stdout, delivery); err != nil {
			return err
		}
		if !delivery.Delivered {
			return errors.New(delivery.Error)
		}
		return nil
	default:
		return errors.New("webhook 子命令: list, add, test")
	}
}

func publicWebhook(webhook automation.WebhookConfig) automation.WebhookConfig {
	if webhook.SecretRef != "" {
		if kind, _, ok := strings.Cut(webhook.SecretRef, ":"); ok {
			webhook.SecretRef = kind + ":[REDACTED]"
		} else {
			webhook.SecretRef = "[REDACTED]"
		}
	}
	for name := range webhook.Headers {
		webhook.Headers[name] = "[REDACTED]"
	}
	return webhook
}

func runSafePoint(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		manifests, err := env.safePoints.List()
		if err != nil {
			return err
		}
		writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "ID\t创建时间\t大小\t原因\t工作副本")
		for _, manifest := range manifests {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n", manifest.ID, manifest.CreatedAt.Local().Format(time.RFC3339), manifest.SizeBytes, manifest.Reason, manifest.WorkingCopy)
		}
		return writer.Flush()
	}
	switch args[0] {
	case "create":
		flags := newFlagSet("safepoint create", stderr)
		path := flags.String("path", ".", "工作副本路径")
		reason := flags.String("reason", "手动创建 SafePoint", "创建原因")
		var include stringList
		flags.Var(&include, "include", "需要包含的未版本化路径（可重复指定）")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		manifest, err := env.safePoints.Create(ctx, *path, safepoint.CreateOptions{Reason: *reason, IncludeUnversioned: include})
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)
	case "inspect":
		if len(args) != 2 {
			return errors.New("用法: svntui safepoint inspect <id>")
		}
		manifest, err := env.safePoints.Inspect(args[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)
	case "validate":
		flags := newFlagSet("safepoint validate", stderr)
		path := flags.String("path", ".", "工作副本路径")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("用法: svntui safepoint validate [--path 工作副本] <id>")
		}
		validation, err := env.safePoints.Validate(ctx, flags.Arg(0), *path)
		if err != nil {
			return err
		}
		return writeJSON(stdout, validation)
	case "restore":
		flags := newFlagSet("safepoint restore", stderr)
		path := flags.String("path", ".", "工作副本路径")
		backup := flags.Bool("backup", true, "恢复前备份当前修改")
		var include stringList
		flags.Var(&include, "backup-include", "恢复前备份需要包含的未版本化路径")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("用法: svntui safepoint restore [--path 工作副本] <id>")
		}
		result, err := env.safePoints.Restore(ctx, flags.Arg(0), *path, safepoint.RestoreOptions{CreateBackup: *backup, BackupUnversioned: include})
		if encodeErr := writeJSON(stdout, result); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	case "cleanup":
		result, err := env.safePoints.Cleanup()
		if encodeErr := writeJSON(stdout, result); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	case "delete":
		if len(args) != 2 {
			return errors.New("用法: svntui safepoint delete <id>")
		}
		return env.safePoints.Delete(args[1])
	default:
		return errors.New("safepoint 子命令: list, create, inspect, validate, restore, cleanup, delete")
	}
}

func parseSchedule(daysText, windowsText string) (automation.Schedule, error) {
	var schedule automation.Schedule
	dayNames := map[string]time.Weekday{
		"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
	}
	for value := range strings.SplitSeq(daysText, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		day, ok := dayNames[value]
		if !ok {
			return schedule, fmt.Errorf("无效星期 %q；使用 mon,tue,...", value)
		}
		schedule.Weekdays = append(schedule.Weekdays, day)
	}
	for value := range strings.SplitSeq(windowsText, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		start, end, ok := strings.Cut(value, "-")
		if !ok {
			return schedule, fmt.Errorf("无效活跃时段 %q", value)
		}
		schedule.Windows = append(schedule.Windows, automation.ActiveWindow{Start: start, End: end})
	}
	return schedule, schedule.Validate()
}

func parseHeaders(values []string) (map[string]string, error) {
	headers := make(map[string]string, len(values))
	for _, value := range values {
		name, content, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("无效 header %q，应为 KEY=VALUE", value)
		}
		headers[strings.TrimSpace(name)] = content
	}
	return headers, nil
}

type stringList []string

type chineseFlagSet struct {
	*flag.FlagSet
	name   string
	output io.Writer
}

func newFlagSet(name string, output io.Writer) *chineseFlagSet {
	base := flag.NewFlagSet(name, flag.ContinueOnError)
	base.SetOutput(io.Discard)
	flags := &chineseFlagSet{FlagSet: base, name: name, output: output}
	base.Usage = flags.writeUsage
	return flags
}

func (f *chineseFlagSet) Parse(arguments []string) error {
	err := f.FlagSet.Parse(arguments)
	if err == nil {
		return nil
	}
	if errors.Is(err, flag.ErrHelp) {
		return errHelpDisplayed
	}
	f.writeUsage()
	message := err.Error()
	if name, ok := strings.CutPrefix(message, "flag provided but not defined: "); ok {
		return usage(fmt.Sprintf("未知选项: %s", name))
	}
	if name, ok := strings.CutPrefix(message, "flag needs an argument: "); ok {
		return usage(fmt.Sprintf("选项缺少参数: %s", name))
	}
	return usage("选项值无效；请根据上方说明检查参数")
}

func (f *chineseFlagSet) writeUsage() {
	command := "svntui"
	if f.name != "" {
		command += " " + f.name
	}
	fmt.Fprintf(f.output, "用法: %s [选项]\n\n选项:\n", command)
	f.VisitAll(func(option *flag.Flag) {
		argument := " 值"
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			argument = ""
		}
		usage := option.Usage
		if showFlagDefault(option.DefValue) {
			usage += "（默认值: " + option.DefValue + "）"
		}
		fmt.Fprintf(f.output, "  -%s%s\n    %s\n", option.Name, argument, usage)
	})
}

func showFlagDefault(value string) bool {
	return value != "" && value != "false" && value != "0" && value != "0s"
}

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func svnVersion(ctx context.Context, client svn.Client) string {
	value, err := client.Version(ctx)
	if err != nil {
		return "不可用"
	}
	return value
}

func printHelp(writer io.Writer) {
	fmt.Fprintf(writer, `svntui %s - Subversion 终端客户端

用法:
  svntui [--no-color] [工作副本]
  svntui web [--port 0] [--no-open] [工作副本]
  svntui status [--remote] [工作副本]
  svntui <commit|add|delete|move|revert|cleanup|lock|unlock|resolve> --path 工作副本 --yes [选项] 目标...
  svntui <update|merge> --path 工作副本 [--apply --yes] [选项] [目标...]
  svntui switch --path 工作副本 --yes URL
  svntui changelist <list|add|remove> [选项] [目标...]
  svntui property <list|get|set|delete|edit> [选项] [参数...]
  svntui externals <list|set|edit> [选项] [参数...]
  svntui patch <export|apply> [选项] [参数...]
  svntui project <list|add|remove|open> [选项] [参数...]
  脚本命令可追加 --json 输出稳定结果封装；Web/TUI 不支持 --json；选项须放在位置参数之前。
  svntui diff [--working-copy 路径] [目标...]
  svntui repository <list|compare|checkout|export|mkdir|delete|move|copy>
  svntui history <log|blame|compare>
  svntui favorite <list|add|remove>
  svntui config <show|set KEY VALUE>
  svntui conflict <edit|merge-tool> [--path 路径] 文件
  svntui automation <list|add|delete|run|run-now|pause|resume|runs>
  svntui automation webhook <list|add|test>
  svntui automation service <print|install|uninstall>
  svntui safepoint <list|create|inspect|validate|restore|cleanup|delete>

TUI 快捷键:
  1-9 / Tab  切换模块             j/k 或方向键   移动光标
  Space      临时选择             d              查看工作区/修订差异
  h          当前文件历史         Enter          查看修订摘要
  :          中文命令列表         ?              查看帮助

执行 "svntui automation add -h" 或在其他子命令后添加 -h 查看详细参数。
`, version)
}
