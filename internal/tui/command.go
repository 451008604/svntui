package tui

import (
	"fmt"
	"strings"
	"unicode"
)

type commandKind uint8

const (
	commandNone commandKind = iota
	commandHelp
	commandQuit
	commandRefresh
	commandDiff
	commandModule
	commandAction
)

type paletteCommand struct {
	kind      commandKind
	module    Module
	action    Action
	arguments []string
	dangerous bool
}

type paletteEntry struct {
	Command     string
	Description string
	Template    string
	Edit        bool
}

var paletteEntries = []paletteEntry{
	{Command: "refresh", Description: "刷新当前页面数据"},
	{Command: "diff", Description: "查看工作区差异，或历史页选中修订的内容差异"},
	{Command: "module workspace", Description: "打开工作区状态页面"},
	{Command: "module repository", Description: "打开版本库路径页面"},
	{Command: "module history", Description: "打开修订历史页面"},
	{Command: "module branches", Description: "打开分支与路径收藏页面"},
	{Command: "module conflicts", Description: "打开冲突处理页面"},
	{Command: "module automations", Description: "打开自动更新任务页面"},
	{Command: "module safepoints", Description: "打开安全快照页面"},
	{Command: "module operations", Description: "打开 SVN 操作记录页面"},
	{Command: "module projects", Description: "选择并切换项目"},
	{Command: "properties", Description: "读取选中路径的 SVN 属性"},
	{Command: "externals", Description: "查看外部依赖"},
	{Command: "changelist <分组>", Description: "设置变更分组", Template: "changelist ", Edit: true},
	{Command: "update", Description: "预览远端变化，不立即更新"},
	{Command: "update --apply", Description: "确认后更新工作副本到 HEAD"},
	{Command: "add", Description: "将选中路径加入版本控制"},
	{Command: "delete", Description: "删除选中路径（高风险）"},
	{Command: "move <目标路径>", Description: "移动当前路径", Template: "move ", Edit: true},
	{Command: "commit <提交说明>", Description: "提交临时选择集中的文件", Template: "commit ", Edit: true},
	{Command: "revert", Description: "还原选中路径并先创建安全快照（高风险）"},
	{Command: "cleanup", Description: "清理当前工作副本锁和中断状态"},
	{Command: "resolve <方式>", Description: "按 working、mine-full、theirs-full 或 base 解决冲突", Template: "resolve working", Edit: true},
	{Command: "lock <说明>", Description: "锁定选中文件，可填写锁定说明", Template: "lock ", Edit: true},
	{Command: "unlock", Description: "解锁选中文件"},
	{Command: "ignore <模式>", Description: "为当前目录添加忽略模式", Template: "ignore ", Edit: true},
	{Command: "property <名称> <值>", Description: "设置选中路径的 SVN 属性", Template: "property ", Edit: true},
	{Command: "copy <源URL> <目标URL> <revision必填> <说明>", Description: "创建分支或标签（远端写操作）", Template: "copy ", Edit: true},
	{Command: "switch <目标URL>", Description: "切换当前工作副本分支（高风险）", Template: "switch ", Edit: true},
	{Command: "merge <参数>", Description: "预览或执行合并，结果只保留在本地", Template: "merge ", Edit: true},
	{Command: "help", Description: "显示键位和操作帮助"},
	{Command: "quit", Description: "退出 svntui"},
	{Command: "手动输入", Description: "直接输入完整命令，适合高级参数", Edit: true},
}

func filteredPaletteEntries(query string) []paletteEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]paletteEntry(nil), paletteEntries...)
	}
	result := make([]paletteEntry, 0, len(paletteEntries))
	for _, entry := range paletteEntries {
		haystack := strings.ToLower(entry.Command + " " + entry.Description + " " + entry.Template)
		if strings.Contains(haystack, query) {
			result = append(result, entry)
		}
	}
	return result
}

func parsePaletteCommand(line string) (paletteCommand, error) {
	fields, err := splitCommand(line)
	if err != nil {
		return paletteCommand{}, err
	}
	if len(fields) == 0 {
		return paletteCommand{}, nil
	}
	name := strings.ToLower(fields[0])
	switch name {
	case "help", "?":
		return paletteCommand{kind: commandHelp}, nil
	case "quit", "q":
		return paletteCommand{kind: commandQuit}, nil
	case "refresh", "r", "status":
		return paletteCommand{kind: commandRefresh}, nil
	case "diff", "d":
		return paletteCommand{kind: commandDiff}, nil
	case "module":
		if len(fields) != 2 {
			return paletteCommand{}, fmt.Errorf("用法: module <页面>")
		}
		module, ok := parseModule(fields[1])
		if !ok {
			return paletteCommand{}, fmt.Errorf("未知页面 %q", fields[1])
		}
		return paletteCommand{kind: commandModule, module: module}, nil
	default:
		if module, ok := parseModule(name); ok && len(fields) == 1 {
			return paletteCommand{kind: commandModule, module: module}, nil
		}
		action, dangerous, ok := parseAction(name)
		if !ok {
			return paletteCommand{}, fmt.Errorf("未知命令 %q；输入 help 查看命令", fields[0])
		}
		return paletteCommand{
			kind:      commandAction,
			action:    action,
			arguments: append([]string(nil), fields[1:]...),
			dangerous: dangerous,
		}, nil
	}
}

func parseModule(value string) (Module, bool) {
	value = strings.ToLower(value)
	for _, module := range Modules {
		if strings.ToLower(module.String()) == value || strings.ToLower(module.DisplayName()) == value {
			return module, true
		}
	}
	return ModuleWorkspace, false
}

func parseAction(value string) (Action, bool, bool) {
	switch Action(value) {
	case ActionUpdate, ActionAdd, ActionMove, ActionCommit, ActionCleanup,
		ActionLock, ActionUnlock, ActionIgnore, ActionProperty, ActionProperties, ActionExternals, ActionChangelist, ActionPropDelete, ActionPatch:
		return Action(value), false, true
	case ActionDelete, ActionRevert, ActionCopy, ActionSwitch, ActionMerge, ActionResolve:
		return Action(value), true, true
	default:
		return "", false, false
	}
}

func splitCommand(line string) ([]string, error) {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false
	hasValue := false

	flush := func() {
		if hasValue {
			fields = append(fields, current.String())
			current.Reset()
			hasValue = false
		}
	}

	runes := []rune(line)
	for index, value := range runes {
		if escaped {
			current.WriteRune(value)
			hasValue = true
			escaped = false
			continue
		}
		if value == '\\' {
			// Windows 路径保持字面反斜杠；普通参数仅转义空格和引号。
			token := current.String()
			windowsPath := (len(token) >= 2 && token[1] == ':') || strings.HasPrefix(token, `\`) || (token == "" && index+1 < len(runes) && runes[index+1] == '\\')
			if windowsPath || quote == '\'' || (index+1 < len(runes) && !unicode.IsSpace(runes[index+1]) && runes[index+1] != '\'' && runes[index+1] != '"') {
				current.WriteRune(value)
				hasValue = true
				continue
			}
			escaped = true
			hasValue = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
				continue
			}
			current.WriteRune(value)
			hasValue = true
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			hasValue = true
			continue
		}
		if unicode.IsSpace(value) {
			flush()
			continue
		}
		current.WriteRune(value)
		hasValue = true
	}
	if escaped {
		return nil, fmt.Errorf("命令末尾存在未完成的转义")
	}
	if quote != 0 {
		return nil, fmt.Errorf("命令中存在未闭合的引号")
	}
	flush()
	return fields, nil
}
