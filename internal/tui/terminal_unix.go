//go:build !windows

package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type ttyTerminal struct {
	file     *os.File
	original string
	close    sync.Once
	closeErr error
}

func openTTY() (terminal, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开终端设备失败: %w", err)
	}

	original, err := stty(file, "-g")
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	term := &ttyTerminal{file: file, original: strings.TrimSpace(original)}
	if _, err := stty(file, "raw", "-echo", "min", "0", "time", "1"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := io.WriteString(file, enterAlternateScreen); err != nil {
		_ = term.Close()
		return nil, fmt.Errorf("进入终端备用屏幕失败: %w", err)
	}
	return term, nil
}

func (t *ttyTerminal) Read(buffer []byte) (int, error) {
	count, err := t.file.Read(buffer)
	return normalizeTTYRead(count, err)
}

// VMIN=0、VTIME>0 的终端读取超时会被 os.File 表示为零字节 EOF，这不是输入流结束。
func normalizeTTYRead(count int, err error) (int, error) {
	if count == 0 && errors.Is(err, io.EOF) {
		return 0, nil
	}
	return count, err
}

func (t *ttyTerminal) Write(buffer []byte) (int, error) { return t.file.Write(buffer) }

func (t *ttyTerminal) Size() (int, int, error) {
	output, err := stty(t.file, "size")
	if err != nil {
		return 0, 0, err
	}
	return parseTerminalSize(output)
}

func (t *ttyTerminal) Close() error {
	t.close.Do(func() {
		_, screenErr := io.WriteString(t.file, leaveAlternateScreen)
		_, restoreErr := stty(t.file, t.original)
		fileErr := t.file.Close()
		t.closeErr = errors.Join(screenErr, restoreErr, fileErr)
	})
	return t.closeErr
}

func stty(file *os.File, args ...string) (string, error) {
	command := exec.Command("stty", args...)
	command.Stdin = file
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return "", fmt.Errorf("配置终端模式失败（stty %s）：%s: %w", strings.Join(args, " "), message, err)
		}
		return "", fmt.Errorf("配置终端模式失败（stty %s）: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

func (t *ttyTerminal) Suspend() error {
	_, screenErr := io.WriteString(t.file, leaveAlternateScreen)
	_, modeErr := stty(t.file, t.original)
	return errors.Join(screenErr, modeErr)
}
func (t *ttyTerminal) Resume() error {
	_, modeErr := stty(t.file, "raw", "-echo", "min", "0", "time", "1")
	_, screenErr := io.WriteString(t.file, enterAlternateScreen)
	return errors.Join(modeErr, screenErr)
}
