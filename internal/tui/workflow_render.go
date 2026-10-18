package tui

import (
	"fmt"
	"strings"
)

func cursorText(text []rune, position int) string {
	position = min(max(position, 0), len(text))
	return string(text[:position]) + "▏" + string(text[position:])
}

func (r renderer) renderForm(m model, width, height int) []string {
	content := m.form.title + "\n"
	for i, field := range m.form.fields {
		prefix := "  "
		value := string(field.value)
		if i == m.form.active {
			prefix = "> "
			value = cursorText(field.value, field.position)
		}
		content += "\n" + prefix + field.label + "：\n    " + value + "\n"
	}
	label := "确认"
	if m.form.action == ActionMerge || m.form.action == ActionUpdate || m.form.action == ActionPatch {
		label = "预览"
	}
	prefix := "  "
	if m.form.active == len(m.form.fields) {
		prefix = "> "
	}
	content += "\n" + prefix + "[" + label + "]"
	lines := wrapText(sanitizeText(content), max(width-2, 1))
	activeLine := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "> ") {
			activeLine = i
			break
		}
	}
	return renderDocument("填写表单", content, max(activeLine-max(height-5, 1), 0), width, height)
}

func (r renderer) renderContext(m model, width, height int) []string {
	app := application{model: m}
	items := app.contextActions()
	cursor := min(m.contextCursor, max(len(items)-1, 0))
	rows := []string{padLine(" 当前项操作 · ↑↓ 选择 · Enter 打开", width)}
	start := max(cursor-max(height-2, 0), 0)
	for i := start; i < len(items) && len(rows) < height; i++ {
		prefix := "  "
		if i == cursor {
			prefix = "> "
		}
		rows = append(rows, padLine(prefix+items[i].label, width))
	}
	return fillRows(rows, width, height)
}

func (r renderer) renderCommit(m model, width, height int) []string {
	// 三块区域同屏显示，焦点由 Tab 切换。
	fileHeight := max(height/3, 2)
	messageHeight := max(height/3, 2)
	diffHeight := max(height-fileHeight-messageHeight, 0)
	if height < 6 {
		return renderDocument("提交草稿", cursorText(m.commit.message, m.commit.position), 0, width, height)
	}
	rows := r.renderWorkspace(m, width, fileHeight)
	fileLabel := " 文件"
	if m.commit.focus == 0 {
		fileLabel = "> 文件"
	}
	rows[0] = padLine(fmt.Sprintf("%s · 可见 %d · 已选 %d · Space 选择 · d 差异", fileLabel, len(m.entries), len(m.selected)), width)
	messageLabel := "提交说明"
	if m.commit.focus == 1 {
		messageLabel = "> " + messageLabel
	}
	message := string(m.commit.message)
	if m.commit.focus == 1 {
		message = cursorText(m.commit.message, m.commit.position)
	}
	offset := 0
	if m.commit.focus == 1 {
		offset = max(len(wrapText(sanitizeText(string(m.commit.message[:min(m.commit.position, len(m.commit.message))])), max(width-2, 1)))-max(messageHeight-1, 1), 0)
	}
	rows = append(rows, renderDocument(messageLabel, message, offset, width, messageHeight)...)
	diffLabel := "差异"
	if m.commit.focus == 2 {
		diffLabel = "> " + diffLabel
	}
	diff := m.commit.diff
	if diff == "" {
		diff = "在文件区域按 d 读取选中文件差异。"
	}
	rows = append(rows, renderDocument(diffLabel, diff, m.commit.diffOffset, width, diffHeight)...)
	return fillRows(rows, width, height)
}
