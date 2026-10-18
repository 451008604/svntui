package tui

import (
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const consoleInputPollInterval = 100 * time.Millisecond

// 保持 Windows INPUT_RECORD 的布局，非键盘事件只读取事件类型。
type consoleInputRecord struct {
	eventType       uint16
	padding         uint16
	keyDown         int32
	repeatCount     uint16
	virtualKey      uint16
	virtualScanCode uint16
	character       uint16
	controlKeyState uint32
}

type consoleInputReader struct {
	pending       []byte
	highSurrogate uint16
}

func (r *consoleInputReader) read(buffer []byte, readEvents func([]consoleInputRecord, time.Duration) (int, error)) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	// 先消费转换后的文本，防止系统队列已空时遗漏多字节字符。
	if len(r.pending) > 0 {
		return r.copyPending(buffer), nil
	}
	deadline := time.Now().Add(consoleInputPollInterval)
	var records [128]consoleInputRecord
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, nil
		}
		count, err := readEvents(records[:], remaining)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			return 0, nil
		}
		r.feed(records[:count])
		if len(r.pending) > 0 {
			return r.copyPending(buffer), nil
		}
		// 非字符事件继续等待到同一期限，保证 Esc 刷新与取消有界。
	}
}

func (r *consoleInputReader) copyPending(buffer []byte) int {
	count := copy(buffer, r.pending)
	r.pending = r.pending[count:]
	return count
}

func (r *consoleInputReader) feed(records []consoleInputRecord) {
	for _, record := range records {
		if record.eventType != 1 || record.character == 0 {
			continue
		}
		// Alt 数字码在松开 Alt 时携带字符，其余按键释放不产生文本。
		if record.keyDown == 0 && record.virtualKey != 0x12 {
			continue
		}
		// VT 模式已把方向键和粘贴边界转为字符事件，原样保留。
		for range max(record.repeatCount, 1) {
			r.appendCharacter(record.character)
		}
	}
}

func (r *consoleInputReader) appendCharacter(character uint16) {
	// 代理对可跨事件批次；只在确认配对失败后输出替换字符。
	if r.highSurrogate != 0 {
		if character >= 0xdc00 && character <= 0xdfff {
			r.pending = utf8.AppendRune(r.pending, utf16.DecodeRune(rune(r.highSurrogate), rune(character)))
			r.highSurrogate = 0
			return
		}
		r.pending = utf8.AppendRune(r.pending, utf8.RuneError)
		r.highSurrogate = 0
	}
	if character >= 0xd800 && character <= 0xdbff {
		r.highSurrogate = character
		return
	}
	r.pending = utf8.AppendRune(r.pending, rune(character))
}
