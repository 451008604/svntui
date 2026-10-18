//go:build windows

package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
	enableMouseInput                = 0x0010
	enableQuickEditMode             = 0x0040
	enableExtendedFlags             = 0x0080
	enableVirtualTerminalInput      = 0x0200
	enableProcessedOutput           = 0x0001
	enableWrapAtEOLOutput           = 0x0002
	enableVirtualTerminalProcessing = 0x0004
	utf8CodePage                    = 65001
	waitObjectSignaled              = 0x00000000
	waitTimeout                     = 0x00000102
	waitFailed                      = 0xffffffff
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	getConsoleModeProc             = kernel32.NewProc("GetConsoleMode")
	setConsoleModeProc             = kernel32.NewProc("SetConsoleMode")
	getConsoleScreenBufferInfoProc = kernel32.NewProc("GetConsoleScreenBufferInfo")
	getConsoleCPProc               = kernel32.NewProc("GetConsoleCP")
	setConsoleCPProc               = kernel32.NewProc("SetConsoleCP")
	getConsoleOutputCPProc         = kernel32.NewProc("GetConsoleOutputCP")
	setConsoleOutputCPProc         = kernel32.NewProc("SetConsoleOutputCP")
	waitForSingleObjectProc        = kernel32.NewProc("WaitForSingleObject")
	readConsoleInputProc           = kernel32.NewProc("ReadConsoleInputW")
)

type consoleCoord struct {
	x int16
	y int16
}

type consoleRect struct {
	left   int16
	top    int16
	right  int16
	bottom int16
}

type consoleScreenBufferInfo struct {
	size              consoleCoord
	cursorPosition    consoleCoord
	attributes        uint16
	window            consoleRect
	maximumWindowSize consoleCoord
}

type ttyTerminal struct {
	input             *os.File
	output            *os.File
	inputMode         uint32
	outputMode        uint32
	inputCodePage     uint32
	outputCodePage    uint32
	inputModeChanged  bool
	outputModeChanged bool
	inputCodeChanged  bool
	outputCodeChanged bool
	alternateScreen   bool
	reader            consoleInputReader
	close             sync.Once
	closeErr          error
}

func openTTY() (terminal, error) {
	input, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 Windows 控制台输入失败: %w", err)
	}
	output, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		_ = input.Close()
		return nil, fmt.Errorf("打开 Windows 控制台输出失败: %w", err)
	}

	term := &ttyTerminal{input: input, output: output}
	if err := term.enableRawMode(); err != nil {
		_ = term.Close()
		return nil, err
	}
	if _, err := io.WriteString(output, enterAlternateScreen); err != nil {
		_ = term.Close()
		return nil, fmt.Errorf("进入终端备用屏幕失败: %w", err)
	}
	term.alternateScreen = true
	return term, nil
}

func (t *ttyTerminal) enableRawMode() error {
	inputMode, err := getConsoleMode(t.input.Fd())
	if err != nil {
		return fmt.Errorf("读取 Windows 控制台输入模式失败: %w", err)
	}
	outputMode, err := getConsoleMode(t.output.Fd())
	if err != nil {
		return fmt.Errorf("读取 Windows 控制台输出模式失败: %w", err)
	}
	t.inputMode = inputMode
	t.outputMode = outputMode

	rawInput := inputMode | enableExtendedFlags | enableVirtualTerminalInput
	rawInput &^= enableProcessedInput | enableLineInput | enableEchoInput | enableMouseInput | enableQuickEditMode
	if err := setConsoleMode(t.input.Fd(), rawInput); err != nil {
		return fmt.Errorf("启用 Windows 控制台原始输入失败: %w", err)
	}
	t.inputModeChanged = true

	virtualOutput := outputMode | enableProcessedOutput | enableWrapAtEOLOutput | enableVirtualTerminalProcessing
	if err := setConsoleMode(t.output.Fd(), virtualOutput); err != nil {
		return fmt.Errorf("启用 Windows 控制台虚拟终端输出失败: %w", err)
	}
	t.outputModeChanged = true

	t.inputCodePage, err = getConsoleCodePage(getConsoleCPProc)
	if err != nil {
		return fmt.Errorf("读取 Windows 控制台输入编码失败: %w", err)
	}
	t.outputCodePage, err = getConsoleCodePage(getConsoleOutputCPProc)
	if err != nil {
		return fmt.Errorf("读取 Windows 控制台输出编码失败: %w", err)
	}
	if err := setConsoleCodePage(setConsoleCPProc, utf8CodePage); err != nil {
		return fmt.Errorf("设置 Windows 控制台输入为 UTF-8 失败: %w", err)
	}
	t.inputCodeChanged = true
	if err := setConsoleCodePage(setConsoleOutputCPProc, utf8CodePage); err != nil {
		return fmt.Errorf("设置 Windows 控制台输出为 UTF-8 失败: %w", err)
	}
	t.outputCodeChanged = true
	return nil
}

func (t *ttyTerminal) Read(buffer []byte) (int, error) {
	return t.reader.read(buffer, t.readConsoleEvents)
}

func (t *ttyTerminal) readConsoleEvents(records []consoleInputRecord, timeout time.Duration) (int, error) {
	// 等待完整输入事件，避免字符读取吞掉非字符事件后继续阻塞。
	milliseconds := max(1, (timeout+time.Millisecond-1)/time.Millisecond)
	result, _, callErr := waitForSingleObjectProc.Call(t.input.Fd(), uintptr(milliseconds))
	switch result {
	case waitObjectSignaled:
		var count uint32
		result, _, callErr = readConsoleInputProc.Call(t.input.Fd(), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&count)))
		if result == 0 {
			return 0, fmt.Errorf("读取 Windows 控制台输入事件失败: %w", windowsCallError(callErr))
		}
		return int(count), nil
	case waitTimeout:
		return 0, nil
	case waitFailed:
		return 0, fmt.Errorf("等待 Windows 控制台输入失败: %w", windowsCallError(callErr))
	default:
		return 0, fmt.Errorf("等待 Windows 控制台输入返回未知状态：0x%x", result)
	}
}

func (t *ttyTerminal) Write(buffer []byte) (int, error) { return t.output.Write(buffer) }

func (t *ttyTerminal) Size() (int, int, error) {
	var info consoleScreenBufferInfo
	result, _, callErr := getConsoleScreenBufferInfoProc.Call(t.output.Fd(), uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return 0, 0, fmt.Errorf("读取 Windows 控制台尺寸失败: %w", windowsCallError(callErr))
	}
	width := int(info.window.right-info.window.left) + 1
	height := int(info.window.bottom-info.window.top) + 1
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("Windows 控制台返回了无效尺寸：%d×%d", width, height)
	}
	return width, height, nil
}

func (t *ttyTerminal) Close() error {
	t.close.Do(func() {
		var closeErrors []error
		if t.alternateScreen {
			if _, err := io.WriteString(t.output, leaveAlternateScreen); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("退出终端备用屏幕失败: %w", err))
			}
		}
		if t.inputModeChanged {
			if err := setConsoleMode(t.input.Fd(), t.inputMode); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("恢复 Windows 控制台输入模式失败: %w", err))
			}
		}
		if t.outputModeChanged {
			if err := setConsoleMode(t.output.Fd(), t.outputMode); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("恢复 Windows 控制台输出模式失败: %w", err))
			}
		}
		if t.inputCodeChanged {
			if err := setConsoleCodePage(setConsoleCPProc, t.inputCodePage); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("恢复 Windows 控制台输入编码失败: %w", err))
			}
		}
		if t.outputCodeChanged {
			if err := setConsoleCodePage(setConsoleOutputCPProc, t.outputCodePage); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("恢复 Windows 控制台输出编码失败: %w", err))
			}
		}
		if err := t.input.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("关闭 Windows 控制台输入失败: %w", err))
		}
		if err := t.output.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("关闭 Windows 控制台输出失败: %w", err))
		}
		t.closeErr = errors.Join(closeErrors...)
	})
	return t.closeErr
}

func getConsoleMode(handle uintptr) (uint32, error) {
	var mode uint32
	result, _, callErr := getConsoleModeProc.Call(handle, uintptr(unsafe.Pointer(&mode)))
	if result == 0 {
		return 0, windowsCallError(callErr)
	}
	return mode, nil
}

func setConsoleMode(handle uintptr, mode uint32) error {
	result, _, callErr := setConsoleModeProc.Call(handle, uintptr(mode))
	if result == 0 {
		return windowsCallError(callErr)
	}
	return nil
}

func getConsoleCodePage(proc *syscall.LazyProc) (uint32, error) {
	codePage, _, callErr := proc.Call()
	if codePage == 0 {
		return 0, windowsCallError(callErr)
	}
	return uint32(codePage), nil
}

func setConsoleCodePage(proc *syscall.LazyProc, codePage uint32) error {
	result, _, callErr := proc.Call(uintptr(codePage))
	if result == 0 {
		return windowsCallError(callErr)
	}
	return nil
}

func windowsCallError(err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return syscall.EINVAL
	}
	return err
}

func (t *ttyTerminal) Suspend() error {
	_, screenErr := io.WriteString(t.output, leaveAlternateScreen)
	return errors.Join(screenErr, setConsoleMode(t.input.Fd(), t.inputMode), setConsoleMode(t.output.Fd(), t.outputMode), setConsoleCodePage(setConsoleCPProc, t.inputCodePage), setConsoleCodePage(setConsoleOutputCPProc, t.outputCodePage))
}
func (t *ttyTerminal) Resume() error {
	raw := t.inputMode | enableExtendedFlags | enableVirtualTerminalInput
	raw &^= enableProcessedInput | enableLineInput | enableEchoInput | enableMouseInput | enableQuickEditMode
	modeErr := errors.Join(setConsoleMode(t.input.Fd(), raw), setConsoleMode(t.output.Fd(), t.outputMode|enableProcessedOutput|enableWrapAtEOLOutput|enableVirtualTerminalProcessing), setConsoleCodePage(setConsoleCPProc, utf8CodePage), setConsoleCodePage(setConsoleOutputCPProc, utf8CodePage))
	_, screenErr := io.WriteString(t.output, enterAlternateScreen)
	return errors.Join(modeErr, screenErr)
}
