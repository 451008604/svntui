package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	enterAlternateScreen = "\x1b[?1049h\x1b[?25l\x1b[?2004h"
	leaveAlternateScreen = "\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

type terminal interface {
	io.Reader
	io.Writer
	Size() (width, height int, err error)
	Close() error
}

type terminalReadInterrupter interface {
	interruptRead()
}

func interruptTerminalRead(term terminal) {
	if interrupter, ok := term.(terminalReadInterrupter); ok {
		interrupter.interruptRead()
	}
}

func parseTerminalSize(output string) (int, int, error) {
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("无法解析终端尺寸 %q", strings.TrimSpace(output))
	}
	height, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("解析终端高度失败: %w", err)
	}
	width, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("解析终端宽度失败: %w", err)
	}
	return width, height, nil
}

// 外部工具运行期间恢复正常输入与主屏幕。
type terminalSuspender interface {
	Suspend() error
	Resume() error
}
