package svn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySymlinkKeepsNodeIdentity(t *testing.T) {
	client, wc, repositoryURL := newFeatureRepository(t)
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(wc, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(wc, "link")
	if err := os.Symlink("first", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := client.Add(t.Context(), wc, []string{"first", "second", "link"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(t.Context(), wc, []string{"first", "second", "link"}, "symlink identity"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second", link); err != nil {
		t.Fatal(err)
	}
	info, err := client.InfoAt(t.Context(), link, "BASE")
	if err != nil || info.URL != repositoryURL+"/link" || info.Kind != "file" {
		t.Fatalf("followed symlink instead of identifying node: %+v %v", info, err)
	}
}
