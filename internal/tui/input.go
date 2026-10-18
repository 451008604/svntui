package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"unicode/utf8"
)

type keyKind uint8

const (
	keyRune keyKind = iota
	keyEnter
	keyEscape
	keyBackspace
	keyDelete
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyPageUp
	keyPageDown
	keyTab
	keyBackTab
	keyCtrlC
	keyCtrlS
	keyCtrlE
	keyCtrlL
	keyPaste
)

type key struct {
	kind keyKind
	rune rune
	text string
}

type inputEvent struct{ key key }

type inputErrorEvent struct{ err error }

type keyDecoder struct {
	pending []byte
	pasting bool
}

func (d *keyDecoder) feed(data []byte) []key {
	d.pending = append(d.pending, data...)
	return d.decode(false)
}

func (d *keyDecoder) flush() []key { return d.decode(true) }

func (d *keyDecoder) decode(flush bool) []key {
	var keys []key
	for len(d.pending) > 0 {
		// 括号粘贴作为一个文本事件处理，换行不会触发命令执行。
		if d.pasting {
			end := bytes.Index(d.pending, []byte("\x1b[201~"))
			if end < 0 {
				break
			}
			keys = append(keys, key{kind: keyPaste, text: string(d.pending[:end])})
			d.pending = d.pending[end+6:]
			d.pasting = false
			continue
		}
		if d.pending[0] == '\x1b' {
			if len(d.pending) == 1 {
				if flush {
					keys = append(keys, key{kind: keyEscape})
					d.pending = d.pending[1:]
				}
				break
			}
			if d.pending[1] != '[' {
				keys = append(keys, key{kind: keyEscape})
				d.pending = d.pending[1:]
				continue
			}
			end := ansiSequenceEnd(d.pending[2:])
			if end < 0 {
				if flush {
					keys = append(keys, key{kind: keyEscape})
					d.pending = d.pending[1:]
				}
				break
			}
			sequence := string(d.pending[2 : end+3])
			if sequence == "200~" {
				d.pasting = true
				d.pending = d.pending[end+3:]
				continue
			}
			if decoded, ok := decodeANSIKey(sequence); ok {
				keys = append(keys, decoded)
			}
			d.pending = d.pending[end+3:]
			continue
		}

		value := d.pending[0]
		switch value {
		case '\r', '\n':
			keys = append(keys, key{kind: keyEnter})
			d.pending = d.pending[1:]
		case '\t':
			keys = append(keys, key{kind: keyTab})
			d.pending = d.pending[1:]
		case '\x13':
			keys = append(keys, key{kind: keyCtrlS})
			d.pending = d.pending[1:]
		case '\x05':
			keys = append(keys, key{kind: keyCtrlE})
			d.pending = d.pending[1:]
		case '\x0c':
			keys = append(keys, key{kind: keyCtrlL})
			d.pending = d.pending[1:]
		case '\x03':
			keys = append(keys, key{kind: keyCtrlC})
			d.pending = d.pending[1:]
		case '\x7f', '\b':
			keys = append(keys, key{kind: keyBackspace})
			d.pending = d.pending[1:]
		default:
			decoded, size := utf8.DecodeRune(d.pending)
			if decoded == utf8.RuneError && size == 1 && !utf8.FullRune(d.pending) && !flush {
				return keys
			}
			keys = append(keys, key{kind: keyRune, rune: decoded})
			d.pending = d.pending[size:]
		}
	}
	return keys
}

func ansiSequenceEnd(data []byte) int {
	for index, value := range data {
		if value >= 0x40 && value <= 0x7e {
			return index
		}
	}
	return -1
}

func decodeANSIKey(sequence string) (key, bool) {
	switch sequence {
	case "A":
		return key{kind: keyUp}, true
	case "B":
		return key{kind: keyDown}, true
	case "C":
		return key{kind: keyRight}, true
	case "D":
		return key{kind: keyLeft}, true
	case "H", "1~", "7~":
		return key{kind: keyHome}, true
	case "F", "4~", "8~":
		return key{kind: keyEnd}, true
	case "3~":
		return key{kind: keyDelete}, true
	case "5~":
		return key{kind: keyPageUp}, true
	case "6~":
		return key{kind: keyPageDown}, true
	case "Z":
		return key{kind: keyBackTab}, true
	default:
		return key{}, false
	}
}

func readInput(ctx context.Context, source io.Reader, events chan<- any) {
	decoder := keyDecoder{}
	buffer := make([]byte, 128)
	for {
		count, err := source.Read(buffer)
		// 挂起终端时的中断错误属于旧读取任务，不应关闭恢复后的界面。
		if ctx.Err() != nil {
			return
		}
		if count > 0 {
			for _, decoded := range decoder.feed(buffer[:count]) {
				if !sendEvent(ctx, events, inputEvent{key: decoded}) {
					return
				}
			}
		}
		if count == 0 && err == nil {
			for _, decoded := range decoder.flush() {
				if !sendEvent(ctx, events, inputEvent{key: decoded}) {
					return
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				sendEvent(ctx, events, inputErrorEvent{err: err})
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func sendEvent(ctx context.Context, events chan<- any, event any) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
