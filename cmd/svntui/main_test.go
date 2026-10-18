package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChineseFlagMessages(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		var output bytes.Buffer
		flags := newFlagSet("status", &output)
		flags.Bool("remote", false, "同时查询远端状态")
		err := flags.Parse([]string{"-h"})
		if !errors.Is(err, errHelpDisplayed) {
			t.Fatalf("Parse() error = %v", err)
		}
		if text := output.String(); !strings.Contains(text, "用法:") || strings.Contains(text, "Usage of") || strings.Contains(text, "(default") {
			t.Fatalf("help output: %s", text)
		}
	})

	t.Run("unknown option", func(t *testing.T) {
		var output bytes.Buffer
		flags := newFlagSet("status", &output)
		err := flags.Parse([]string{"--unknown"})
		if err == nil || !strings.Contains(err.Error(), "未知选项") {
			t.Fatalf("Parse() error = %v", err)
		}
		if strings.Contains(output.String(), "flag provided") {
			t.Fatalf("error output: %s", output.String())
		}
	})
}

func TestHeadlessWorkflow(t *testing.T) {
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))

	repository := filepath.Join(root, "repository")
	workingCopy := filepath.Join(root, "working-copy")
	runCommand(t, root, "svnadmin", "create", repository)
	runCommand(t, root, "svn", "checkout", repositoryURL(repository), workingCopy)
	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, workingCopy, "svn", "add", "hello.txt")
	runCommand(t, workingCopy, "svn", "commit", "-m", "initial")

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if err := run(t.Context(), []string{"status", workingCopy}, stdout, stderr); err != nil {
		t.Fatalf("status: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "状态") {
		t.Fatalf("status output: %s", stdout.String())
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"favorite", "add", "--name", "main", repositoryURL(repository)}, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run(t.Context(), []string{"favorite", "list"}, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "main") {
		t.Fatalf("favorite output: %s", stdout.String())
	}

	stdout.Reset()
	if err := run(t.Context(), []string{"automation", "add", "--name", "main update", "--path", workingCopy, "--interval", "1m"}, stdout, stderr); err != nil {
		t.Fatalf("automation add: %v\n%s", err, stderr.String())
	}
	stdout.Reset()
	if err := run(t.Context(), []string{"automation", "list"}, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "main update") {
		t.Fatalf("automation output: %s", stdout.String())
	}

	if err := os.WriteFile(filepath.Join(workingCopy, "hello.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run(t.Context(), []string{"safepoint", "create", "--path", workingCopy, "--reason", "test"}, stdout, stderr); err != nil {
		t.Fatalf("safepoint create: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"reason": "test"`) {
		t.Fatalf("safepoint output: %s", stdout.String())
	}
	stdout.Reset()
	if err := run(t.Context(), []string{"safepoint", "list"}, stdout, stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "test") {
		t.Fatalf("safepoint list: %s", stdout.String())
	}
}

func TestVersionDoesNotRequireWritableConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing", "config"))
	var output bytes.Buffer
	if err := run(t.Context(), []string{"version"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "svntui "+version) {
		t.Fatalf("version output: %s", output.String())
	}
}

func runCommand(t *testing.T, dir, binary string, args ...string) {
	t.Helper()
	if binary == "svn" {
		args = append([]string{"--non-interactive"}, args...)
	}
	command := exec.CommandContext(t.Context(), binary, args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, output)
	}
}

func repositoryURL(path string) string {
	if runtime.GOOS == "windows" {
		return "file:///" + filepath.ToSlash(path)
	}
	return "file://" + filepath.ToSlash(path)
}
