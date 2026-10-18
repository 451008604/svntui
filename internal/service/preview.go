package service

import (
	"encoding/json"
	"net/url"
	"strings"

	"svntui/internal/domain"
)

// 展示规范化后的实际操作范围，避免界面选择与执行目标不一致。
func (a Actions) PreviewAction(request domain.ActionRequest) string {
	normalized, err := a.NormalizeAction(request)
	if err != nil {
		return "参数无效: " + err.Error()
	}
	args := append([]string(nil), normalized.Arguments...)
	for i, arg := range args {
		if normalized.Action == domain.ActionProperty && i == 1 && sensitiveName(args[0]) {
			args[i] = "[REDACTED]"
		} else {
			args[i] = sanitizePreviewArgument(arg)
		}
	}
	encoded, _ := json.Marshal(args)
	targets, _ := json.Marshal(normalized.Targets)
	mode := "写入操作"
	if normalized.IsReadOnly() {
		mode = "只读预览"
	}
	text := "操作: " + normalized.Action.DisplayName() + "（" + mode + "）\n工作目录: " + normalized.WorkingCopy + "\n目标: " + string(targets) + "\n参数: " + string(encoded)
	if normalized.Revision != "" {
		text += "\n目标修订: " + normalized.Revision
	}
	if normalized.Depth != "" {
		text += "\n检出深度: " + normalized.Depth
	}
	if normalized.Action == domain.ActionMerge || normalized.Action == domain.ActionSwitch {
		text += "\n影响范围: 整个工作副本；合并不会自动提交"
	}
	return text
}

func sanitizePreviewArgument(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.IsAbs() && parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
		return parsed.String()
	}
	return value
}

func sensitiveName(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "password") || strings.Contains(value, "secret") || strings.Contains(value, "token") || strings.Contains(value, "auth")
}
