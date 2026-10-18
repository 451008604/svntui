package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestFavoriteRejectsEmbeddedPassword(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	var output bytes.Buffer
	err := run(t.Context(), []string{"favorite", "add", "--name", "test", "https://user:private-secret@example.test/repo"}, &output, &output)
	if err == nil || strings.Contains(err.Error()+output.String(), "private-secret") {
		t.Fatalf("embedded password was accepted or echoed: %v", err)
	}
	env, err := newEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if len(env.config.Favorites) != 0 {
		t.Fatal("rejected favorite was persisted")
	}
	if err := run(t.Context(), []string{"favorite", "add", "--name", "test", "https://user@example.test/repo"}, &output, &output); err != nil {
		t.Fatalf("username-only URL should remain usable: %v", err)
	}
}
