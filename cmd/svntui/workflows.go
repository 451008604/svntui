package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/config"
	"svntui/internal/domain"
	"svntui/internal/service"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

type jsonOutputKey struct{}

func wantsJSON(ctx context.Context) bool { value, _ := ctx.Value(jsonOutputKey{}).(bool); return value }

type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }
func usage(message string) error    { return &usageError{message} }

func errorCategory(err error) string {
	if err == nil {
		return ""
	}
	var commandErr *svn.CommandError
	if errors.As(err, &commandErr) {
		return string(commandErr.Kind)
	}
	var inputErr *usageError
	if errors.As(err, &inputErr) {
		return "invalid_arguments"
	}
	if errors.Is(err, wclock.ErrBusy) {
		return "working_copy_locked"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "operation_failed"
}

func commandExitCode(err error) int {
	switch errorCategory(err) {
	case "":
		return 0
	case "invalid_arguments":
		return 2
	case "authentication", "permission":
		return 3
	case "conflict", "out_of_date", "working_copy_locked":
		return 4
	case "network":
		return 5
	case "canceled":
		return 130
	default:
		return 1
	}
}

// JSON 只改变输出形式；位置参数和消息中的同名文本必须原样保留。
func jsonArguments(args []string) ([]string, bool) {
	result := make([]string, 0, len(args))
	enabled := false
	literal := false
	commandWords := 1
	var command string
	values := map[string]bool{"path": true, "working-copy": true, "message": true, "m": true, "message-file": true, "revision": true, "depth": true, "name": true, "group": true, "search": true, "author": true, "since": true, "until": true, "limit": true, "before": true, "changelist": true, "accept": true, "output": true, "value-file": true, "left": true, "right": true, "reason": true, "include": true, "backup-include": true, "url": true, "secret-ref": true, "header": true, "interval": true, "days": true, "windows": true, "timezone": true, "webhook": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if literal {
			result = append(result, arg)
			continue
		}
		if arg == "--" {
			literal = true
			result = append(result, arg)
			continue
		}
		if arg == "--json" || arg == "--json=true" {
			enabled = true
			continue
		}
		if arg == "--json=false" {
			enabled = false
			continue
		}
		result = append(result, arg)
		if !strings.HasPrefix(arg, "-") {
			if commandWords > 0 {
				commandWords--
				if command == "" {
					command = arg
					if slices.Contains([]string{"changelist", "property", "externals", "patch", "project", "repository", "history", "favorite", "config", "conflict", "automation", "safepoint"}, command) {
						commandWords++
					}
				} else if command == "automation" && (arg == "webhook" || arg == "service") {
					commandWords++
				}
			} else {
				// 与 Go flag 一致：第一个位置参数之后均按字面值保留。
				literal = true
			}
			continue
		}
		key := strings.TrimLeft(arg, "-")
		if strings.HasPrefix(arg, "-") && values[key] && i+1 < len(args) {
			i++
			result = append(result, args[i])
		}
	}
	return result, enabled
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args, asJSON := jsonArguments(args)
	if !asJSON {
		return runInternal(ctx, args, stdout, stderr)
	}
	var output bytes.Buffer
	err := runInternal(context.WithValue(ctx, jsonOutputKey{}, true), args, &output, stderr)
	if errors.Is(err, errHelpDisplayed) {
		err = nil
	}
	var data any
	if json.Unmarshal(output.Bytes(), &data) != nil {
		data = strings.TrimRight(output.String(), "\n")
	}
	response := map[string]any{"ok": err == nil, "data": data, "error": nil, "exit_code": commandExitCode(err)}
	if err != nil {
		response["error"] = map[string]string{"kind": errorCategory(err), "message": err.Error()}
	}
	return errors.Join(err, writeJSON(stdout, response))
}

func actionService(env environment) service.Actions {
	return service.Actions{Client: env.client, SafePoints: env.safePoints, LockDir: filepath.Join(env.dirs.State, "working-copy-locks"), ExpertMode: env.config.ExpertMode}
}

func executeCLI(ctx context.Context, env environment, r domain.ActionRequest, yes bool, stdout io.Writer) error {
	actions := actionService(env)
	request, err := actions.NormalizeAction(r)
	if err != nil {
		return usage(err.Error())
	}
	if !request.IsReadOnly() && !yes {
		return usage("写操作需要 --yes；请先复查目标。update/merge/patch 默认为预览")
	}
	result, err := actions.Execute(ctx, request)
	if wantsJSON(ctx) {
		return errors.Join(err, writeJSON(stdout, result))
	}
	if result.Output != "" {
		fmt.Fprintln(stdout, result.Output)
	}
	if result.Summary != "" {
		fmt.Fprintln(stdout, result.Summary)
	}
	return err
}

func runWorkspaceAction(ctx context.Context, env environment, name string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet(name, stderr)
	path := flags.String("path", ".", "工作副本路径")
	yes := flags.Bool("yes", false, "确认执行写操作")
	var message, messageFile, group, revision, depth, accept string
	var apply, reverse bool
	switch name {
	case "commit":
		flags.StringVar(&message, "message", "", "提交说明")
		flags.StringVar(&message, "m", "", "提交说明")
		flags.StringVar(&messageFile, "message-file", "", "从 UTF-8 文件读取多行提交说明")
		flags.StringVar(&group, "changelist", "", "提交此原生变更分组")
	case "update":
		flags.BoolVar(&apply, "apply", false, "执行更新（默认预览）")
		flags.StringVar(&revision, "revision", "", "目标修订")
		flags.StringVar(&depth, "depth", "", "调整检出深度")
	case "merge":
		flags.BoolVar(&apply, "apply", false, "执行合并（默认预演）")
		flags.BoolVar(&reverse, "reverse", false, "反向合并")
		flags.StringVar(&revision, "revision", "", "修订或修订列表")
	case "lock":
		flags.StringVar(&message, "message", "", "锁定说明")
	case "resolve":
		flags.StringVar(&accept, "accept", "working", "working/mine-full/theirs-full/base")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	r := domain.ActionRequest{Action: domain.Action(name), WorkingCopy: *path, Targets: flags.Args(), Revision: revision, Depth: depth}
	switch name {
	case "commit":
		if messageFile != "" {
			if message != "" {
				return usage("--message 和 --message-file 不能同时使用")
			}
			data, err := os.ReadFile(messageFile)
			if err != nil {
				return err
			}
			message = string(data)
		}
		if strings.TrimSpace(message) == "" {
			return usage("提交需要 --message 或 --message-file")
		}
		r.Arguments = []string{message}
		if group != "" {
			entries, err := env.client.Status(ctx, *path, false, false)
			if err != nil {
				return err
			}
			selected := make([]string, 0)
			for _, entry := range entries {
				if entry.Changelist == group && (len(r.Targets) == 0 || slices.Contains(r.Targets, entry.Path)) {
					selected = append(selected, entry.Path)
				}
			}
			r.Targets = selected
		}
	case "update":
		if apply {
			r.Arguments = []string{"--apply"}
		}
	case "merge":
		if flags.NArg() != 1 {
			return usage("merge 需要一个源 URL")
		}
		r.Arguments = []string{flags.Arg(0)}
		r.Targets = nil
		if revision != "" {
			r.Arguments = append(r.Arguments, revision)
		}
		if reverse {
			r.Arguments = append(r.Arguments, "--reverse")
		}
		if apply {
			r.Arguments = append(r.Arguments, "--apply")
		}
	case "switch":
		if flags.NArg() != 1 {
			return usage("switch 需要一个目标 URL")
		}
		r.Arguments = flags.Args()
		r.Targets = nil
	case "move":
		r.Arguments = flags.Args()
		r.Targets = nil
	case "lock":
		r.Arguments = []string{message}
	case "resolve":
		r.Arguments = []string{accept}
	}
	return executeCLI(ctx, env, r, *yes, stdout)
}

func runChangelist(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	flags := newFlagSet("changelist "+args[0], stderr)
	path := flags.String("path", ".", "工作副本路径")
	yes := flags.Bool("yes", false, "确认变更分组")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		entries, err := env.client.Status(ctx, *path, false, false)
		if err != nil {
			return err
		}
		groups := map[string][]string{}
		for _, entry := range entries {
			if entry.Changelist != "" {
				groups[entry.Changelist] = append(groups[entry.Changelist], entry.Path)
			}
		}
		return writeJSON(stdout, groups)
	case "add":
		if flags.NArg() < 2 {
			return usage("用法: changelist add --path 工作副本 --yes 分组名 文件...")
		}
		return executeCLI(ctx, env, domain.ActionRequest{Action: domain.ActionChangelist, WorkingCopy: *path, Targets: flags.Args()[1:], Arguments: []string{flags.Arg(0)}}, *yes, stdout)
	case "remove":
		return executeCLI(ctx, env, domain.ActionRequest{Action: domain.ActionChangelist, WorkingCopy: *path, Targets: flags.Args(), Arguments: []string{"--remove"}}, *yes, stdout)
	default:
		return usage("changelist 子命令: list, add, remove")
	}
}

func runProperty(ctx context.Context, env environment, kind string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	mode := args[0]
	flags := newFlagSet(kind+" "+mode, stderr)
	path := flags.String("path", ".", "工作副本路径")
	yes := flags.Bool("yes", false, "确认属性修改")
	valueFile := flags.String("value-file", "", "从文件读取多行属性值")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	operands := flags.Args()
	property := ""
	if kind == "externals" {
		property = "svn:externals"
	}
	if mode == "list" {
		action := domain.ActionProperties
		if kind == "externals" {
			action = domain.ActionExternals
		}
		return executeCLI(ctx, env, domain.ActionRequest{Action: action, WorkingCopy: *path, Targets: operands}, false, stdout)
	}
	if property == "" {
		if len(operands) == 0 {
			return usage("需要属性名称")
		}
		property = operands[0]
		operands = operands[1:]
	}
	if mode == "get" {
		if len(operands) > 1 {
			return usage("get 只接受一个目标")
		}
		target := "."
		if len(operands) == 1 {
			target = operands[0]
		}
		if _, err := secureWorkingCopyPath(*path, target); err != nil {
			return err
		}
		value, err := env.client.PropGet(ctx, *path, property, target)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, value)
		return err
	}
	r := domain.ActionRequest{WorkingCopy: *path, Action: domain.ActionProperty}
	switch mode {
	case "delete":
		r.Action = domain.ActionPropDelete
		r.Arguments = []string{property}
		r.Targets = operands
	case "set":
		var value string
		if *valueFile != "" {
			data, err := os.ReadFile(*valueFile)
			if err != nil {
				return err
			}
			value = string(data)
		} else {
			if len(operands) == 0 {
				return usage("set 需要属性值或 --value-file")
			}
			value = operands[0]
			operands = operands[1:]
		}
		r.Arguments = []string{property, value}
		r.Targets = operands
	case "edit":
		if !*yes {
			return usage("属性编辑保存需要 --yes")
		}
		if len(operands) != 1 {
			return usage("edit 需要一个明确目标")
		}
		props, err := env.client.Properties(ctx, *path, operands[0])
		if err != nil {
			return err
		}
		file, err := os.CreateTemp("", "svntui-property-*.txt")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err := file.WriteString(props[property]); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := runExternal(ctx, env, domain.ExternalRequest{Kind: "editor", WorkingCopy: *path, Target: file.Name()}, stdout, stderr); err != nil {
			return err
		}
		data, err := os.ReadFile(file.Name())
		if err != nil {
			return err
		}
		r.Arguments = []string{property, string(data)}
		r.Targets = operands
	default:
		return usage("property 子命令: list, get, set, delete, edit")
	}
	return executeCLI(ctx, env, r, *yes, stdout)
}

func runPatch(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage("patch 子命令: export, apply")
	}
	flags := newFlagSet("patch "+args[0], stderr)
	path := flags.String("path", ".", "工作副本路径")
	output := flags.String("output", "", "导出补丁路径")
	force := flags.Bool("force", false, "允许覆盖已有补丁文件")
	apply := flags.Bool("apply", false, "执行应用（默认预演）")
	yes := flags.Bool("yes", false, "确认应用补丁")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "export":
		if *output == "" {
			return usage("导出需要 --output")
		}
		if _, err := os.Stat(*output); err == nil && !*force {
			return usage("输出文件已存在，覆盖需要 --force")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, target := range flags.Args() {
			if _, err := secureWorkingCopyPath(*path, target); err != nil {
				return err
			}
		}
		if err := env.client.NativeDiffToFile(ctx, *path, flags.Args(), *output); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "补丁已导出:", *output)
		return nil
	case "apply":
		if flags.NArg() != 1 {
			return usage("apply 需要补丁文件路径")
		}
		arguments := []string{flags.Arg(0)}
		if *apply {
			arguments = append(arguments, "--apply")
		}
		return executeCLI(ctx, env, domain.ActionRequest{Action: domain.ActionPatch, WorkingCopy: *path, Arguments: arguments}, *yes, stdout)
	default:
		return usage("patch 子命令: export, apply")
	}
}

func runProject(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	flags := newFlagSet("project "+args[0], stderr)
	name := flags.String("name", "", "项目名称")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	modules := service.Modules{Client: env.client, Config: env.config, ConfigStore: env.configStore}
	switch args[0] {
	case "list":
		projects, err := modules.Projects(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, projects)
	case "add":
		if flags.NArg() != 1 {
			return usage("project add 需要一个工作副本路径")
		}
		info, err := env.client.Info(ctx, flags.Arg(0))
		if err != nil {
			return err
		}
		if info.WCAbsolute == "" {
			return usage("指定路径不是工作副本")
		}
		if *name == "" {
			*name = filepath.Base(info.WCAbsolute)
		}
		return env.configStore.Update(func(cfg *config.Config) error {
			return cfg.AddProject(config.Project{Name: *name, Path: info.WCAbsolute})
		})
	case "remove":
		if flags.NArg() != 1 {
			return usage("project remove 需要名称或路径")
		}
		value := flags.Arg(0)
		return env.configStore.Update(func(cfg *config.Config) error {
			paths := []string{value}
			for _, p := range cfg.Projects {
				if p.Name == value {
					paths = append(paths, p.Path)
				}
			}
			cfg.Projects = slices.DeleteFunc(cfg.Projects, func(p config.Project) bool { return p.Name == value || slices.Contains(paths, p.Path) })
			cfg.Recent = slices.DeleteFunc(cfg.Recent, func(p string) bool { return slices.Contains(paths, p) })
			return nil
		})
	case "open":
		if flags.NArg() != 1 {
			return usage("project open 需要名称或路径")
		}
		value := flags.Arg(0)
		for _, p := range env.config.Projects {
			if p.Name == value {
				value = p.Path
				break
			}
		}
		return runTUI(ctx, env, []string{value})
	default:
		return usage("project 子命令: list, add, remove, open")
	}
}

func runExternal(ctx context.Context, env environment, r domain.ExternalRequest, stdout, stderr io.Writer) error {
	if r.Kind != "editor" {
		return runConflict(ctx, env, []string{r.Kind, "--path", r.WorkingCopy, r.Target}, stdout, stderr)
	}
	editor := env.config.Editor
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return errors.New("请配置 editor 或 EDITOR 环境变量")
	}
	command := exec.CommandContext(ctx, editor, r.Target)
	command.Dir = r.WorkingCopy
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	return errors.Join(ctx.Err(), err)
}
