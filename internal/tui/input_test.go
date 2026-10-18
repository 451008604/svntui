package tui

import (
	"reflect"
	"testing"
)

func TestKeyDecoderHandlesSplitSequencesAndUTF8(t *testing.T) {
	decoder := keyDecoder{}
	if got := decoder.feed([]byte{'\x1b', '['}); len(got) != 0 {
		t.Fatalf("partial ANSI sequence produced keys: %#v", got)
	}
	got := decoder.feed([]byte{'A', ' ', 0xe4, 0xb8})
	want := []key{{kind: keyUp}, {kind: keyRune, rune: ' '}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first feed = %#v, want %#v", got, want)
	}
	got = decoder.feed([]byte{0xad})
	want = []key{{kind: keyRune, rune: '中'}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("second feed = %#v, want %#v", got, want)
	}
}

func TestKeyDecoderFlushesStandaloneEscape(t *testing.T) {
	decoder := keyDecoder{}
	decoder.feed([]byte{'\x1b'})
	want := []key{{kind: keyEscape}}
	if got := decoder.flush(); !reflect.DeepEqual(got, want) {
		t.Fatalf("flush() = %#v, want %#v", got, want)
	}
}
