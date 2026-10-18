package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"svntui/internal/svn"
)

const helpText = `全局键位
  1-9        切换模块
  Tab        下一个模块
  j/k ↑/↓    移动游标或滚动详情
  g/G        列表首项/末项
  :          打开命令面板
  ?          显示帮助
  Esc        关闭面板
  q / Ctrl-C 退出

工作区
  Space      切换当前项的临时选择
  A/I/X      全选可见/反选可见/清空选择
  /          文件名、目录、状态和变更分组筛选
  a          上下文操作（移动、属性、分组等）
  C          独立提交页
  r          异步刷新状态
  d / Enter  异步读取选中项或当前项的 diff
  h          查看当前文件的修订历史
  c          取消正在运行的读取任务

历史
  Enter      查看选中修订的说明和变更路径
  d          按需读取选中修订涉及的实际内容差异

提交页
  Tab        切换文件、说明、差异区域
  Space      选择文件；文件区 d/Enter 读取差异
  Enter      在说明区域插入换行
  Ctrl-S     确认提交；Ctrl-E 外部编辑说明
  Esc        返回并保留本项目草稿

详情
  Home/End   首尾；PageUp/PageDown 翻页
  / 与 n/N   搜索与下一个/上一个匹配
  [ / ]      上一个/下一个 diff hunk
  a          预演成功后进入应用确认

历史、版本库和项目
  [ / ]      历史上一页/下一页（每页 100）
  /          历史作者、说明、路径、日期筛选
  b / v      逐行追溯与源码 / 比较任意两个修订
  Enter      版本库进入目录；项目页切换项目
  Backspace  版本库返回父目录
  e/m        冲突页外部编辑/合并工具

合并向导和属性
  Ctrl-L     合并表单读取可合并/已合并修订
  Space      修订列表勾选；A 全选、X 清空
  Enter      修订列表确认；属性值内插入换行
  Ctrl-S     提交表单；合并先预演，随后按 a 确认应用

命令面板
  输入文字   按命令名或中文说明筛选
  ↑/↓        选择命令
  Enter      执行命令，或进入参数编辑
  ←/→ Home/End  参数编辑光标
  ↑/↓        参数编辑时浏览命令历史
  Tab        参数编辑时补全命令
  Esc        参数编辑时返回列表，再次按下关闭面板

写操作先展示确认页，确认后以结构化请求交给外部操作服务。`

type renderer struct{ color bool }

const tableSeparator = " │ "

type tableLayout struct {
	widths []int
}

func (r renderer) render(m model) string {
	width, height := m.width, m.height
	bodyHeight := max(height-4, 1)
	rows := make([]string, 0, height)

	header := " svntui"
	if m.workingCopy != "" {
		header += "  " + m.workingCopy
	}
	rows = append(rows, r.paint(padLine(header, width), "1;37;44"))
	rows = append(rows, r.moduleTabs(m.module, width))

	var body []string
	switch m.overlay {
	case overlayCommand:
		body = r.renderCommandPalette(m, width, bodyHeight)
	case overlayHelp:
		body = renderDocument("帮助", helpText, m.overlayOffset, width, bodyHeight)
	case overlayDetail:
		body = renderDocument(m.detailTitle, m.detailContent, m.overlayOffset, width, bodyHeight)
	case overlayConfirm:
		body = renderConfirmation(m.confirm, width, bodyHeight)
	case overlayCommit:
		body = r.renderCommit(m, width, bodyHeight)
	case overlayForm:
		body = r.renderForm(m, width, bodyHeight)
	case overlayContext:
		body = r.renderContext(m, width, bodyHeight)
	case overlayRevisions:
		body = r.renderRevisions(m, width, bodyHeight)
	default:
		if m.module == ModuleWorkspace {
			body = r.renderWorkspace(m, width, bodyHeight)
		} else if data, ok := m.moduleData[m.module]; ok {
			body = r.renderModuleData(m, data, width, bodyHeight)
		} else {
			body = renderEmptyModule(m.module, width, bodyHeight)
		}
	}
	rows = append(rows, body...)
	for len(rows) < height-2 {
		rows = append(rows, strings.Repeat(" ", width))
	}

	status := m.message
	if m.busy {
		status = fmt.Sprintf("执行中: %s · %s", m.busyLabel, time.Since(m.busySince).Truncate(time.Second))
		if m.cancelable {
			status += "（Ctrl-C 取消）"
		}
	}
	rows = append(rows, r.paint(padLine(" "+status, width), "30;47"))
	if m.overlay == overlayCommand {
		if m.commandEditing {
			rows = append(rows, padLine(" 命令："+cursorText(m.command, m.commandPosition)+"  · Enter 执行 · Esc 返回列表", width))
		} else {
			rows = append(rows, padLine(" 搜索："+string(m.command)+"  · ↑/↓ 选择 · Enter 确认 · Esc 关闭", width))
		}
	} else if m.overlay == overlayConfirm {
		if m.confirm.dangerous {
			rows = append(rows, r.paint(padLine(" 输入 YES 后按 Enter 确认 · Esc 取消: "+string(m.confirm.typed), width), "1;31"))
		} else {
			rows = append(rows, r.paint(padLine(" y 确认 · n/Esc 取消", width), "1;33"))
		}
	} else if m.detailSearching {
		rows = append(rows, padLine(" 搜索："+string(m.detailSearch)+" · Enter 查找 · Esc 返回", width))
	} else if m.overlay == overlayCommit {
		rows = append(rows, padLine(" Tab 区域 · Ctrl-S 提交 · Ctrl-E 编辑器 · Esc 保存草稿", width))
	} else if m.overlay == overlayForm {
		rows = append(rows, padLine(" Tab 字段 · Enter 下一项/换行 · Ctrl-S 提交 · Ctrl-L 修订", width))
	} else if m.overlay == overlayRevisions {
		rows = append(rows, padLine(" Space 勾选 · A 全选 · X 清空 · Enter 使用选择 · Esc 返回", width))
	} else {
		rows = append(rows, padLine(" 1-9 模块 · a 操作 · C 提交 · / 筛选 · : 命令 · ? 帮助", width))
	}

	if len(rows) > height {
		rows = rows[:height]
	}
	return "\x1b[H\x1b[2J" + strings.Join(rows, "\r\n")
}

func (r renderer) renderCommandPalette(m model, width, height int) []string {
	if m.commandEditing {
		content := "请补全命令参数，然后按 Enter 执行。\n\n" + string(m.command) + "\n\n命令将先经过解析和风险确认，不会作为 shell 文本直接执行。"
		return renderDocument("编辑命令参数", content, 0, width, height)
	}

	entries := filteredPaletteEntries(string(m.command))
	rows := []string{padLine(" 命令面板（可输入中文或英文筛选）", width)}
	if len(entries) == 0 {
		rows = append(rows, padLine(" 没有匹配的命令；按 Backspace 修改搜索条件。", width))
		return fillRows(rows, width, height)
	}
	data := ModuleData{Columns: []string{"指令", "中文说明"}, Rows: make([]ModuleRow, 0, len(entries))}
	for _, entry := range entries {
		data.Rows = append(data.Rows, ModuleRow{Cells: []string{entry.Command, entry.Description}})
	}
	layout := newTableLayout(data, max(width-2, 1))
	rows = append(rows, r.paint(padLine("  "+layout.render(data.Columns), width), "1;36"))
	available := max(height-len(rows), 1)
	cursor := min(m.commandCursor, len(entries)-1)
	start := 0
	if cursor >= available {
		start = cursor - available + 1
	}
	for index := start; index < min(start+available, len(entries)); index++ {
		prefix := "  "
		if index == cursor {
			prefix = "> "
		}
		line := padLine(prefix+layout.render(data.Rows[index].Cells), width)
		if index == cursor {
			line = r.paint(line, "30;47")
		}
		rows = append(rows, line)
	}
	return fillRows(rows, width, height)
}

func (r renderer) renderModuleData(m model, data ModuleData, width, height int) []string {
	rows := make([]string, 0, height)
	title := data.Title
	if title == "" {
		title = m.module.DisplayName()
	}
	rows = append(rows, padLine(" "+title, width))
	layout := newTableLayout(data, max(width-2, 1))
	if len(data.Columns) > 0 {
		rows = append(rows, r.paint(padLine("  "+layout.render(data.Columns), width), "1;36"))
	}
	if len(data.Rows) == 0 {
		message := data.EmptyMessage
		if message == "" {
			message = "暂无数据"
		}
		rows = append(rows, padLine(" "+message, width))
		return fillRows(rows, width, height)
	}

	available := max(height-len(rows), 1)
	cursor := min(m.moduleCursor[m.module], len(data.Rows)-1)
	start := 0
	if cursor >= available {
		start = cursor - available + 1
	}
	end := min(start+available, len(data.Rows))
	for index := start; index < end; index++ {
		prefix := "  "
		if index == cursor {
			prefix = "> "
		}
		line := padLine(prefix+layout.render(data.Rows[index].Cells), width)
		if index == cursor {
			line = r.paint(line, "30;47")
		}
		rows = append(rows, line)
	}
	return fillRows(rows, width, height)
}

func newTableLayout(data ModuleData, available int) tableLayout {
	columnCount := len(data.Columns)
	for _, row := range data.Rows {
		columnCount = max(columnCount, len(row.Cells))
	}
	if columnCount == 0 {
		return tableLayout{}
	}

	separatorWidth := displayWidth(tableSeparator)
	maxColumns := max((available+separatorWidth)/(separatorWidth+1), 1)
	columnCount = min(columnCount, maxColumns)
	natural := make([]int, columnCount)
	for index := range natural {
		natural[index] = 1
	}
	measure := func(cells []string) {
		for index := range min(len(cells), columnCount) {
			natural[index] = max(natural[index], displayWidth(sanitizeTableCell(cells[index])))
		}
	}
	measure(data.Columns)
	for _, row := range data.Rows {
		measure(row.Cells)
	}

	cellBudget := max(available-separatorWidth*(columnCount-1), columnCount)
	if sumWidths(natural) <= cellBudget {
		return tableLayout{widths: natural}
	}

	widths := make([]int, columnCount)
	for index, naturalWidth := range natural {
		widths[index] = min(naturalWidth, 4)
	}
	if sumWidths(widths) > cellBudget {
		for index := range widths {
			widths[index] = 1
		}
	}
	for remaining := cellBudget - sumWidths(widths); remaining > 0; {
		progressed := false
		for index := range widths {
			if remaining == 0 {
				break
			}
			if widths[index] >= natural[index] {
				continue
			}
			widths[index]++
			remaining--
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return tableLayout{widths: widths}
}

func (l tableLayout) render(cells []string) string {
	if len(l.widths) == 0 {
		return ""
	}
	formatted := make([]string, len(l.widths))
	for index, width := range l.widths {
		value := ""
		if index < len(cells) {
			value = cells[index]
		}
		formatted[index] = fitTableCell(value, width)
	}
	return strings.Join(formatted, tableSeparator)
}

func fitTableCell(value string, width int) string {
	value = sanitizeTableCell(value)
	if displayWidth(value) > width {
		if width == 1 {
			return "…"
		}
		value = trimWidth(value, width-1) + "…"
	}
	return value + strings.Repeat(" ", max(width-displayWidth(value), 0))
}

func sanitizeTableCell(value string) string {
	return strings.Join(strings.Fields(sanitizeText(value)), " ")
}

func sumWidths(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	return total
}

func (r renderer) moduleTabs(active Module, width int) string {
	labels := make([]string, len(Modules))
	total := 0
	for index, module := range Modules {
		labels[index] = fmt.Sprintf(" %d %s ", index+1, module.DisplayName())
		total += displayWidth(labels[index])
	}
	start, end := 0, len(Modules)
	if total > width {
		start = int(active)
		end = start + 1
		used := displayWidth(labels[start])
		for end < len(labels) && used+displayWidth(labels[end]) <= width {
			used += displayWidth(labels[end])
			end++
		}
		for start > 0 && used+displayWidth(labels[start-1]) <= width {
			start--
			used += displayWidth(labels[start])
		}
	}
	var line strings.Builder
	for index := start; index < end; index++ {
		if Modules[index] == active {
			line.WriteString(r.paint(labels[index], "1;30;46"))
		} else {
			line.WriteString(labels[index])
		}
	}
	return padStyledLine(line.String(), width)
}

func (r renderer) renderWorkspace(m model, width, height int) []string {
	header := padLine(fmt.Sprintf(" 可见 %d/%d · 已选 %d · / 筛选 · A/I/X 选择", len(m.entries), len(m.allEntries), len(m.selected)), width)
	height = max(height-1, 0)
	if len(m.entries) == 0 {
		return append([]string{header}, fillRows([]string{padLine(" 无匹配状态项，按 / 修改筛选或 r 刷新。", width)}, width, height)...)
	}
	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}
	end := min(start+height, len(m.entries))
	rows := make([]string, 0, height)
	for index := start; index < end; index++ {
		entry := m.entries[index]
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		selected := " "
		if m.selected[entry.Path] {
			selected = "x"
		}
		group := ""
		if entry.Changelist != "" {
			group = " {" + entry.Changelist + "}"
		}
		line := fmt.Sprintf("%s [%s] %-3s %s%s%s", cursor, selected, statusCode(entry), entry.Path, group, statusSuffix(entry))
		line = padLine(line, width)
		if index == m.cursor {
			line = r.paint(line, "30;47")
		} else if entry.IsConflict() {
			line = r.paint(line, "1;31")
		}
		rows = append(rows, line)
	}
	return append([]string{header}, fillRows(rows, width, height)...)
}

func (r renderer) paint(value, code string) string {
	if !r.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func renderDocument(title, content string, offset, width, height int) []string {
	lines := wrapText(sanitizeText(content), max(width-2, 1))
	offset = min(max(offset, 0), max(len(lines)-max(height-1, 0), 0))
	rows := []string{padLine(" "+title+"  （Esc 关闭，j/k 滚动）", width)}
	available := max(height-1, 0)
	end := min(offset+available, len(lines))
	for _, line := range lines[offset:end] {
		rows = append(rows, padLine(" "+line, width))
	}
	return fillRows(rows, width, height)
}

func confirmationContent(confirm confirmation) string {
	level := "写操作"
	if confirm.dangerous {
		level = "高风险操作"
	}
	displayTargets := confirm.request.Targets
	if len(displayTargets) == 0 {
		displayTargets = []string{"当前工作副本"}
		if confirm.request.Action == ActionCopy && len(confirm.request.Arguments) >= 2 {
			displayTargets = confirm.request.Arguments[:2]
		}
		if confirm.request.Action == ActionSwitch && len(confirm.request.Arguments) >= 1 {
			displayTargets = confirm.request.Arguments[:1]
		}
	}
	content := fmt.Sprintf("%s：%s\n目标数量：%d\n目标：\n  %s", level, confirm.request.Action.DisplayName(), len(displayTargets), strings.Join(displayTargets, "\n  "))
	if len(confirm.request.Arguments) > 0 && confirm.preview == "" {
		content += "\n参数：" + strings.Join(confirm.request.Arguments, " ")
	}
	if confirm.preview != "" {
		content += "\n\n实际命令摘要:\n" + confirm.preview
	}
	return content
}

func renderConfirmation(confirm confirmation, width, height int) []string {
	return renderDocument("确认操作 · ↑↓/PgUp/PgDn/Home/End 查看目标", confirmationContent(confirm), confirm.offset, width, height)
}

func renderEmptyModule(module Module, width, height int) []string {
	rows := []string{
		padLine(" "+module.DisplayName(), width),
		padLine(" "+moduleHint(module), width),
		padLine(" 按 r 读取数据，按 ? 查看快捷键。", width),
	}
	return fillRows(rows, width, height)
}

func statusCode(entry svn.StatusEntry) string {
	code := "?"
	switch entry.Item {
	case "normal", "none":
		code = " "
	case "modified":
		code = "M"
	case "added":
		code = "A"
	case "deleted":
		code = "D"
	case "missing":
		code = "!"
	case "replaced":
		code = "R"
	case "conflicted":
		code = "C"
	case "unversioned":
		code = "?"
	case "ignored":
		code = "I"
	case "external":
		code = "X"
	case "obstructed":
		code = "~"
	case "incomplete":
		code = "!"
	}
	if entry.Properties == "modified" {
		code += "P"
	} else if entry.Properties == "conflicted" {
		code += "C"
	}
	return code
}

func statusSuffix(entry svn.StatusEntry) string {
	var suffix []string
	if entry.Switched {
		suffix = append(suffix, "已切换")
	}
	if entry.TreeConflicted {
		suffix = append(suffix, "树冲突")
	}
	if entry.LockOwner != "" {
		suffix = append(suffix, "锁定者："+entry.LockOwner)
	}
	if len(suffix) == 0 {
		return ""
	}
	return "  [" + strings.Join(suffix, ", ") + "]"
}

func fillRows(rows []string, width, height int) []string {
	if len(rows) > height {
		return rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, strings.Repeat(" ", width))
	}
	return rows
}

func wrapText(text string, width int) []string {
	var result []string
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			result = append(result, "")
			continue
		}
		for displayWidth(line) > width {
			prefix, rest := splitWidth(line, width)
			result = append(result, prefix)
			line = rest
		}
		result = append(result, line)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func padLine(value string, width int) string {
	value = trimWidth(sanitizeText(value), width)
	return value + strings.Repeat(" ", max(width-displayWidth(value), 0))
}

func padStyledLine(value string, width int) string {
	plainWidth := displayWidth(stripANSI(value))
	if plainWidth > width {
		return trimStyled(value, width)
	}
	return value + strings.Repeat(" ", width-plainWidth)
}

func trimStyled(value string, width int) string {
	var output strings.Builder
	visible := 0
	for index := 0; index < len(value) && visible < width; {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			end := index + 2
			for end < len(value) && !(value[end] >= 0x40 && value[end] <= 0x7e) {
				end++
			}
			if end < len(value) {
				output.WriteString(value[index : end+1])
				index = end + 1
				continue
			}
		}
		decoded, size := utf8.DecodeRuneInString(value[index:])
		runeWidth := displayRuneWidth(decoded)
		if visible+runeWidth > width {
			break
		}
		output.WriteRune(decoded)
		visible += runeWidth
		index += size
	}
	output.WriteString("\x1b[0m")
	return output.String()
}

func trimWidth(value string, width int) string {
	prefix, _ := splitWidth(value, width)
	return prefix
}

func splitWidth(value string, width int) (string, string) {
	used := 0
	for index, decoded := range value {
		runeWidth := displayRuneWidth(decoded)
		if used+runeWidth > width {
			return value[:index], value[index:]
		}
		used += runeWidth
	}
	return value, ""
}

func displayWidth(value string) int {
	width := 0
	for _, decoded := range value {
		width += displayRuneWidth(decoded)
	}
	return width
}

func displayRuneWidth(value rune) int {
	if value == 0 || unicode.Is(unicode.Mn, value) || unicode.IsControl(value) {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe6f) ||
		(value >= 0xff00 && value <= 0xff60) || (value >= 0xffe0 && value <= 0xffe6) ||
		(value >= 0x1f300 && value <= 0x1faff) || (value >= 0x20000 && value <= 0x3fffd)) {
		return 2
	}
	return 1
}

func sanitizeText(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) && !(value[index] >= 0x40 && value[index] <= 0x7e) {
				index++
			}
			if index < len(value) {
				index++
			}
			continue
		}
		decoded, size := utf8.DecodeRuneInString(value[index:])
		index += size
		switch decoded {
		case '\n':
			output.WriteByte('\n')
		case '\t':
			output.WriteString("    ")
		case '\r':
		default:
			if !unicode.IsControl(decoded) {
				output.WriteRune(decoded)
			}
		}
	}
	return output.String()
}

func stripANSI(value string) string {
	return sanitizeText(value)
}

func statusCountMessage(entries []svn.StatusEntry) string {
	conflicts := 0
	for _, entry := range entries {
		if entry.IsConflict() {
			conflicts++
		}
	}
	message := strconv.Itoa(len(entries)) + " 个状态项"
	if conflicts > 0 {
		message += "，" + strconv.Itoa(conflicts) + " 个冲突"
	}
	return message
}
