package web

import (
	"strings"
	"unicode/utf8"
)

const diagnosticLimit = 16 << 10
const diagnosticStageLimit = 4 << 10
const diagnosticTruncation = "\n[诊断已截断]"
const outputTruncation = "\n[任务输出已截断]"

// 各阶段先保留独立标签，再分别限制诊断，避免一个长错误挤掉其他失败信息。
type stageFailure struct {
	label string
	cause error
}

func (e *stageFailure) Error() string {
	return boundedDiagnostic(e.label+": "+diagnosticMessage(e.cause, diagnosticStageLimit-len(e.label)-4), diagnosticStageLimit)
}
func (e *stageFailure) Unwrap() error { return e.cause }
func stageError(label string, err error) error {
	if err == nil {
		return nil
	}
	return &stageFailure{label: label, cause: err}
}

// Join 的每个原因独享预算；不先拼接可能包含数 MiB stderr 的原始错误。
func diagnosticMessage(err error, budget int) string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		parts := make([]string, 0, len(causes))
		share := max((budget-2-len(causes))/max(len(causes), 1), 0)
		for _, cause := range causes {
			if cause != nil {
				parts = append(parts, diagnosticMessage(cause, share))
			}
		}
		return boundedDiagnostic(strings.Join(parts, "\n"), budget)
	}
	return boundedDiagnostic(err.Error(), budget)
}

// 按 JSON 字符串的实际转义成本限长，构造独立 UTF-8 字符串而不保留原始大缓冲。
func boundedDiagnostic(message string, budget int) string {
	text, complete := diagnosticPrefix(message, max(budget-2, 0))
	if complete {
		return text
	}
	markerCost := 0
	for _, r := range diagnosticTruncation {
		markerCost += jsonRuneCost(r)
	}
	prefix, _ := diagnosticPrefix(text, max(budget-2-markerCost, 0))
	if markerCost > max(budget-2, 0) {
		marker, _ := diagnosticPrefix(diagnosticTruncation, max(budget-2, 0))
		return marker
	}
	return prefix + diagnosticTruncation
}
func diagnosticPrefix(message string, budget int) (string, bool) {
	var result strings.Builder
	result.Grow(min(len(message), budget))
	used := 0
	for _, r := range message {
		cost := jsonRuneCost(r)
		if used+cost > budget {
			return result.String(), false
		}
		result.WriteRune(r)
		used += cost
	}
	return result.String(), true
}
func jsonRuneCost(r rune) int {
	switch r {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2
	case '<', '>', '&', '\u2028', '\u2029':
		return 6
	}
	if r < 0x20 {
		return 6
	}
	return utf8.RuneLen(r)
}

// 输出保留既定字节预算、有效 UTF-8 与可见截断标记，且不引用被舍弃的大字符串。
func boundedJobOutput(output string) string {
	if len(output) <= outputLimit && utf8.ValidString(output) {
		return output
	}
	budget := outputLimit - len(outputTruncation)
	var result strings.Builder
	result.Grow(min(len(output), outputLimit))
	for _, r := range output {
		if result.Len()+utf8.RuneLen(r) > budget {
			break
		}
		result.WriteRune(r)
	}
	result.WriteString(outputTruncation)
	return result.String()
}
