package tui

import (
	"strings"
	"testing"
)

func TestParseTerminalSize(t *testing.T) {
	width, height, err := parseTerminalSize(" 24 80\n")
	if err != nil {
		t.Fatalf("解析终端尺寸失败: %v", err)
	}
	if width != 80 || height != 24 {
		t.Fatalf("终端尺寸 = %d×%d，期望 80×24", width, height)
	}
}

func TestParseTerminalSizeReturnsChineseError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "未知", want: "无法解析终端尺寸"},
		{input: "高 80", want: "解析终端高度失败"},
		{input: "24 宽", want: "解析终端宽度失败"},
	}
	for _, test := range tests {
		_, _, err := parseTerminalSize(test.input)
		if err == nil {
			t.Fatalf("%q 未返回错误", test.input)
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Fatalf("错误提示不是中文：%q", err)
		}
	}
}
