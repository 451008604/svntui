package svn

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPropertiesPreserveValueBytes(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	const target = "property-target@"
	writeFeatureFile(t, filepath.Join(wc, target), "content\n")
	if _, err := client.Add(t.Context(), wc, []string{target}); err != nil {
		t.Fatal(err)
	}
	binary := make([]byte, 256)
	for index := range binary {
		binary[index] = byte(index)
	}
	tests := []struct {
		name  string
		value string
	}{
		{name: "unicode", value: "第一行\nsecond line 😀\n"},
		{name: "binary", value: string(binary)},
		{name: "empty", value: ""},
		{name: "line-endings", value: "first\r\nsecond\rthird\n\t "},
		{name: "xml-text", value: "<&>\"'\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := "test:" + test.name
			if _, err := client.PropSet(t.Context(), wc, name, test.value, target); err != nil {
				t.Fatal(err)
			}
			// 原生严格输出确认写入字节，避免读写转换互相掩盖。
			raw, err := client.Run(t.Context(), wc, "propget", "--strict", "--", name, LiteralTarget(target))
			if err != nil || raw.Stdout != test.value {
				t.Fatalf("stored value = %q, %v; want %q", raw.Stdout, err, test.value)
			}
			value, err := client.PropGet(t.Context(), wc, name, target)
			if err != nil || value != test.value {
				t.Fatalf("PropGet = %q, %v; want %q", value, err, test.value)
			}
			properties, err := client.Properties(t.Context(), wc, target)
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := properties[name]; !ok || value != test.value {
				t.Fatalf("Properties[%q] = %q, present %v; want %q", name, value, ok, test.value)
			}
		})
	}
}

func TestSVNTextPropertyUsesUTF8(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	const value = "缓存目录\nemoji-😀\n"
	var valueFile string
	client.Observer = func(command Command, result Result, err error) {
		if len(command.Args) == 0 || command.Args[0] != "propset" {
			return
		}
		fileIndex := slices.Index(command.Args, "--file")
		if fileIndex < 0 || fileIndex+1 >= len(command.Args) {
			t.Errorf("propset must read value from file: %q", command.Args)
			return
		}
		valueFile = command.Args[fileIndex+1]
		data, readErr := os.ReadFile(valueFile)
		if readErr != nil || string(data) != value {
			t.Errorf("property file = %q, %v; want %q", data, readErr, value)
		}
		encodingIndex := slices.Index(command.Args, "--encoding")
		if encodingIndex < 0 || encodingIndex+1 >= len(command.Args) || command.Args[encodingIndex+1] != "UTF-8" {
			t.Errorf("svn: property must specify UTF-8: %q", command.Args)
		}
	}
	if _, err := client.PropSet(t.Context(), wc, "svn:ignore", value, "."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(valueFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary property file was not removed: %v", err)
	}
	got, err := client.PropGet(t.Context(), wc, "svn:ignore", ".")
	if err != nil || got != value {
		t.Fatalf("PropGet = %q, %v; want %q", got, err, value)
	}
}

func TestPropSetTargetsPreservesBytes(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	targets := []string{"first@", "second"}
	for _, target := range targets {
		writeFeatureFile(t, filepath.Join(wc, target), "content\n")
	}
	if _, err := client.Add(t.Context(), wc, targets); err != nil {
		t.Fatal(err)
	}
	const value = "中文\x00\xff\r\n"
	if _, err := client.PropSetTargets(t.Context(), wc, "test:multiple", value, targets); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		got, err := client.PropGet(t.Context(), wc, "test:multiple", target)
		if err != nil || got != value {
			t.Errorf("property for %q = %q, %v; want %q", target, got, err, value)
		}
	}
	properties, err := client.Properties(t.Context(), wc, ".")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := properties["test:multiple"]; ok {
		t.Fatal("unselected root received property")
	}
}

func TestPropGetMissingPropertyPreservesCommandError(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	value, err := client.PropGet(t.Context(), wc, "test:missing", ".")
	var commandErr *CommandError
	if value != "" || !errors.As(err, &commandErr) {
		t.Fatalf("missing property = %q, %v; want CommandError", value, err)
	}
	if commandErr.Result.ExitCode == 0 || !strings.Contains(commandErr.Result.Stderr, "W200017") {
		t.Fatalf("missing property error lost SVN status: %+v", commandErr)
	}
}
