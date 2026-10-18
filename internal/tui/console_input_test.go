package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"
)

func consoleTextRecords(text string) []consoleInputRecord {
	var records []consoleInputRecord
	for _, character := range utf16.Encode([]rune(text)) {
		records = append(records, consoleInputRecord{eventType: 1, keyDown: 1, repeatCount: 1, character: character})
	}
	return records
}

func TestConsoleInputRecordLayout(t *testing.T) {
	var record consoleInputRecord
	if unsafe.Sizeof(record) != 20 || unsafe.Offsetof(record.keyDown) != 4 || unsafe.Offsetof(record.character) != 14 || unsafe.Offsetof(record.controlKeyState) != 16 {
		t.Fatal("console input record does not match the Windows ABI")
	}
}

func TestConsoleInputReaderDrainsUTF8BeforePolling(t *testing.T) {
	reader := consoleInputReader{}
	want := "\x1b[200~" + strings.Repeat("中", 100) + "\x1b[201~"
	records := consoleTextRecords(want)
	reads := 0
	readEvents := func(buffer []consoleInputRecord, _ time.Duration) (int, error) {
		reads++
		if reads > 1 {
			t.Fatal("polled the console while converted input remained buffered")
		}
		return copy(buffer, records), nil
	}
	var got strings.Builder
	buffer := make([]byte, 128)
	for got.Len() < len(want) {
		count, err := reader.read(buffer, readEvents)
		if err != nil || count == 0 {
			t.Fatalf("read = %d, %v", count, err)
		}
		got.Write(buffer[:count])
	}
	if got.String() != want || reads != 1 {
		t.Fatalf("paste changed or read more than once: %q, %d", got.String(), reads)
	}
}

func TestConsoleInputReaderPreservesControlKeysVTAndRepeats(t *testing.T) {
	reader := consoleInputReader{}
	reader.feed(consoleTextRecords("\x03\x13\x1a\x1b[A\x1b[Z"))
	reader.feed([]consoleInputRecord{{eventType: 1, keyDown: 1, repeatCount: 3, character: '中'}})
	buffer := make([]byte, 128)
	count, err := reader.read(buffer, func([]consoleInputRecord, time.Duration) (int, error) {
		t.Fatal("buffered controls should not poll")
		return 0, nil
	})
	if want := "\x03\x13\x1a\x1b[A\x1b[Z中中中"; err != nil || string(buffer[:count]) != want {
		t.Fatalf("controls, VT keys or repeats changed: %q, %v", buffer[:count], err)
	}
}

func TestConsoleInputReaderPreservesSplitSurrogates(t *testing.T) {
	reader := consoleInputReader{}
	pair := consoleTextRecords("😀")
	reads := 0
	buffer := make([]byte, 128)
	count, err := reader.read(buffer, func(records []consoleInputRecord, _ time.Duration) (int, error) {
		records[0] = pair[reads]
		reads++
		return 1, nil
	})
	if err != nil || string(buffer[:count]) != "😀" || reads != 2 {
		t.Fatalf("split surrogate pair = %q, %v, reads %d", buffer[:count], err, reads)
	}
	reader.feed([]consoleInputRecord{{eventType: 1, keyDown: 1, repeatCount: 1, character: 0xd800}})
	reader.feed(consoleTextRecords("A"))
	reader.feed([]consoleInputRecord{{eventType: 1, keyDown: 1, repeatCount: 1, character: 0xdc00}})
	if string(reader.pending) != "�A�" {
		t.Fatalf("invalid surrogate handling = %q", reader.pending)
	}
}

func TestConsoleInputReaderPreservesAltNumpadRelease(t *testing.T) {
	reader := consoleInputReader{}
	reader.feed([]consoleInputRecord{
		{eventType: 1, keyDown: 0, repeatCount: 1, virtualKey: 'X', character: 'x'},
		{eventType: 1, keyDown: 0, repeatCount: 1, virtualKey: 0x12, character: 'é'},
		{eventType: 1, keyDown: 0, repeatCount: 1, virtualKey: 0x12},
	})
	if string(reader.pending) != "é" {
		t.Fatalf("Alt release character lost or ordinary key release repeated: %q", reader.pending)
	}
}

func TestConsoleInputReaderIgnoresNonCharacterEventsUntilTimeout(t *testing.T) {
	reader := consoleInputReader{}
	reads := 0
	previousTimeout := consoleInputPollInterval
	count, err := reader.read(make([]byte, 128), func(records []consoleInputRecord, timeout time.Duration) (int, error) {
		if timeout <= 0 || timeout > previousTimeout {
			t.Fatalf("poll deadline was reset: %v after %v", timeout, previousTimeout)
		}
		previousTimeout = timeout
		reads++
		if reads == 1 {
			return copy(records, []consoleInputRecord{
				{eventType: 4},
				{eventType: 1, keyDown: 0, character: 'x'},
				{eventType: 1, keyDown: 1, virtualKey: 0x10},
			}), nil
		}
		return 0, nil
	})
	if count != 0 || err != nil || reads != 2 {
		t.Fatalf("non-character input = %d, %v, reads %d", count, err, reads)
	}
}

type consoleInputTestSource struct {
	reader     consoleInputReader
	readEvents func([]consoleInputRecord, time.Duration) (int, error)
}

func (s *consoleInputTestSource) Read(buffer []byte) (int, error) {
	return s.reader.read(buffer, s.readEvents)
}

func TestConsoleInputLoopCancelsDuringNonCharacterStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{}, 1)
	source := &consoleInputTestSource{readEvents: func(records []consoleInputRecord, _ time.Duration) (int, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		records[0] = consoleInputRecord{eventType: 4}
		return 1, nil
	}}
	done := make(chan struct{})
	go func() { defer close(done); readInput(ctx, source, make(chan any, 1)) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("input loop did not stop during a non-character event stream")
	}
}

func TestConsoleInputLoopFlushesEscapeAfterNonCharacterEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reads := 0
	source := &consoleInputTestSource{readEvents: func(records []consoleInputRecord, _ time.Duration) (int, error) {
		reads++
		switch reads {
		case 1:
			return copy(records, consoleTextRecords("\x1b")), nil
		case 2:
			records[0] = consoleInputRecord{eventType: 4}
			return 1, nil
		default:
			return 0, nil
		}
	}}
	events := make(chan any, 1)
	done := make(chan struct{})
	go func() { defer close(done); readInput(ctx, source, events) }()
	select {
	case event := <-events:
		if input, ok := event.(inputEvent); !ok || input.key.kind != keyEscape {
			t.Fatalf("unexpected input after Escape: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("Escape was not flushed after non-character input")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("input loop did not stop after Escape")
	}
}

func TestConsoleInputReaderReturnsErrorsAndAcceptsEmptyBuffer(t *testing.T) {
	reader := consoleInputReader{}
	want := errors.New("console unavailable")
	readEvents := func([]consoleInputRecord, time.Duration) (int, error) { return 0, want }
	if count, err := reader.read(nil, readEvents); count != 0 || err != nil {
		t.Fatalf("empty read = %d, %v", count, err)
	}
	if count, err := reader.read(make([]byte, 1), readEvents); count != 0 || !errors.Is(err, want) {
		t.Fatalf("read error = %d, %v", count, err)
	}
}
