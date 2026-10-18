//go:build !windows

package tui

import (
	"errors"
	"io"
	"testing"
)

func TestNormalizeTTYReadTreatsTimeoutAsNoInput(t *testing.T) {
	count, err := normalizeTTYRead(0, io.EOF)
	if count != 0 || err != nil {
		t.Fatalf("终端读取超时 = (%d, %v)，期望 (0, nil)", count, err)
	}
}

func TestNormalizeTTYReadPreservesRealResults(t *testing.T) {
	testErr := errors.New("读取失败")
	tests := []struct {
		count int
		err   error
	}{
		{count: 1, err: io.EOF},
		{count: 0, err: testErr},
		{count: 2, err: nil},
	}
	for _, test := range tests {
		count, err := normalizeTTYRead(test.count, test.err)
		if count != test.count || !errors.Is(err, test.err) {
			t.Fatalf("normalizeTTYRead(%d, %v) = (%d, %v)", test.count, test.err, count, err)
		}
	}
}
