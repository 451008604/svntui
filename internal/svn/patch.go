package svn

import "strings"

// 读取 SVN 的通知类型，避免将文件名中的 conflict/skipped 当成失败。
func PatchHasProblems(output string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Skipped '") || trimmed == "Summary of conflicts:" {
			return true
		}
		if strings.HasPrefix(trimmed, ">") && strings.Contains(trimmed, "rejected hunk") {
			return true
		}
		// 文本和属性的冲突标志位于通知行前两列。
		if len(line) >= 3 && line[2] == ' ' && (line[0] == 'C' || line[1] == 'C') {
			return true
		}
	}
	return false
}
