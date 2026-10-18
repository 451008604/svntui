package safepoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"svntui/internal/svn"
)

func TestRestoreProtectsDescendantsBeforeAnyRemoval(t *testing.T) {
	for _, removal := range []string{"missing", "deleted", "dirty-added", "dirty-replaced"} {
		t.Run(removal, func(t *testing.T) {
			wc := newWorkingCopy(t)
			writeFile(t, filepath.Join(wc, "folder", "base.txt"), []byte("base\n"))
			run(t, wc, "svn", "add", "folder")
			run(t, wc, "svn", "propset", "svn:ignore", "*.ignored", "folder")
			run(t, wc, "svn", "commit", "-m", "add folder")
			manager := testManager(t)
			switch removal {
			case "missing":
				if err := os.RemoveAll(filepath.Join(wc, "folder")); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				run(t, wc, "svn", "delete", "folder")
			}
			manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
			if removal == "dirty-added" {
				if err := os.Mkdir(filepath.Join(wc, "new-folder"), 0o700); err != nil {
					t.Fatal(err)
				}
				run(t, wc, "svn", "add", "new-folder")
			}
			folder := "folder"
			if removal == "dirty-added" {
				folder = "new-folder"
			}
			if removal == "dirty-replaced" {
				run(t, wc, "svn", "delete", "folder")
				if err := os.Mkdir(filepath.Join(wc, "folder"), 0o700); err != nil {
					t.Fatal(err)
				}
				run(t, wc, "svn", "add", "folder")
			}
			if strings.HasPrefix(removal, "dirty-") {
				run(t, wc, "svn", "propset", "svn:ignore", "*.ignored", folder)
				run(t, wc, "svn", "propset", "test:dirty", "yes", ".")
			}
			writeFile(t, filepath.Join(wc, folder, "notes", "deep.txt"), []byte("local notes\n"))
			writeFile(t, filepath.Join(wc, folder, "secret.ignored"), []byte("ignored data\n"))
			if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); !errors.Is(err, ErrDirtyWorkingCopy) {
				t.Fatalf("restore without backup = %v, want dirty rejection", err)
			}
			assertFile(t, filepath.Join(wc, folder, "notes", "deep.txt"), []byte("local notes\n"))
			result, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{CreateBackup: true})
			if err != nil {
				t.Fatal(err)
			}
			backup, err := manager.Inspect(result.BackupID)
			if err != nil {
				t.Fatal(err)
			}
			entries := entriesByPath(backup.Entries)
			for _, path := range []string{folder + "/notes", folder + "/secret.ignored"} {
				entry, ok := entries[path]
				if !ok || entry.Payload == "" || !entry.Unversioned {
					t.Fatalf("unprotected %s: %+v", path, entry)
				}
			}
			assertFile(t, filepath.Join(manager.Root(), backup.ID, entries[folder+"/notes"].Payload, "deep.txt"), []byte("local notes\n"))
			assertFile(t, filepath.Join(manager.Root(), backup.ID, entries[folder+"/secret.ignored"].Payload), []byte("ignored data\n"))
			run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
			if _, err := manager.Restore(t.Context(), backup.ID, wc, RestoreOptions{}); err != nil {
				t.Fatalf("restore backup: %v", err)
			}
			assertFile(t, filepath.Join(wc, folder, "notes", "deep.txt"), []byte("local notes\n"))
			assertFile(t, filepath.Join(wc, folder, "secret.ignored"), []byte("ignored data\n"))
			statuses, err := manager.readStatus(t.Context(), wc)
			if err != nil {
				t.Fatal(err)
			}
			byPath := statusByPath(statuses)
			if byPath[filepath.Join(folder, "notes")].WC.Item != "unversioned" || byPath[filepath.Join(folder, "secret.ignored")].WC.Item != "ignored" {
				t.Fatalf("restored local files changed versioning state: %+v", statuses)
			}
		})
	}
}

func TestRestoreStopsWhenDescendantCannotBeProtected(t *testing.T) {
	wc := newWorkingCopy(t)
	writeFile(t, filepath.Join(wc, "folder", "base.txt"), []byte("base\n"))
	run(t, wc, "svn", "add", "folder")
	run(t, wc, "svn", "commit", "-m", "add folder")
	if err := os.RemoveAll(filepath.Join(wc, "folder")); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, wc, "svn", "revert", "--depth", "infinity", ".")
	writeFile(t, filepath.Join(wc, "folder", "notes", "keep.txt"), []byte("keep\n"))
	fifo := filepath.Join(wc, "folder", "notes", "pipe")
	if err := makeTestFIFO(fifo); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{CreateBackup: true}); err == nil {
		t.Fatal("restore accepted an incomplete backup")
	}
	assertFile(t, filepath.Join(wc, "folder", "notes", "keep.txt"), []byte("keep\n"))
	assertFile(t, filepath.Join(wc, "folder", "base.txt"), []byte("base\n"))
	if _, err := os.Lstat(fifo); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreBinaryOnlySnapshot(t *testing.T) {
	wc := newWorkingCopy(t)
	want := []byte{0, 20, 30, 40, 255}
	writeFile(t, filepath.Join(wc, "binary.dat"), want)
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, wc, "svn", "revert", "binary.dat")
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(wc, "binary.dat"), want)
	if status := run(t, wc, "svn", "status"); !strings.Contains(status, "M       binary.dat") {
		t.Fatal(status)
	}
}

func TestRestoreReplacementRoundTrip(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			wc := newWorkingCopy(t)
			name := "replace@node"
			if directory {
				writeFile(t, filepath.Join(wc, name, "old.txt"), []byte("old\n"))
			} else {
				writeFile(t, filepath.Join(wc, name), []byte("old\n"))
			}
			run(t, wc, "svn", "add", svn.LiteralTarget(name))
			run(t, wc, "svn", "commit", "-m", "old node")
			oldRevision := strings.TrimSpace(run(t, wc, "svn", "info", "--show-item", "revision", svn.LiteralTarget(name)))
			run(t, wc, "svn", "delete", svn.LiteralTarget(name))
			path := name
			if directory {
				path += "/new@file.txt"
			}
			writeFile(t, filepath.Join(wc, path), []byte("replacement\n"))
			run(t, wc, "svn", "add", svn.LiteralTarget(name))
			run(t, wc, "svn", "propset", "test:replacement", "value", svn.LiteralTarget(name))
			manager := testManager(t)
			manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			entry := entriesByPath(manifest.Entries)[name]
			if entry.BaseRevision <= 0 || entry.BaseURL == "" || entry.RepositoryUUID == "" {
				t.Fatalf("missing BASE identity at r%s: %+v", oldRevision, entry)
			}
			run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
			if directory {
				if err := os.RemoveAll(filepath.Join(wc, path)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
				t.Fatal(err)
			}
			assertFile(t, filepath.Join(wc, path), []byte("replacement\n"))
			if property := strings.TrimSpace(run(t, wc, "svn", "propget", "test:replacement", svn.LiteralTarget(name))); property != "value" {
				t.Fatalf("property = %q", property)
			}
			statuses, err := manager.readStatus(t.Context(), wc)
			if err != nil {
				t.Fatal(err)
			}
			if statusByPath(statuses)[name].WC.Item != "replaced" {
				t.Fatalf("status = %+v", statuses)
			}
		})
	}
}

func TestCaptureAndRestoreChangelistAndLiteralPaths(t *testing.T) {
	wc := newWorkingCopy(t)
	name := "note@revision.txt"
	writeFile(t, filepath.Join(wc, name), []byte("base\n"))
	run(t, wc, "svn", "add", svn.LiteralTarget(name))
	run(t, wc, "svn", "commit", "-m", "literal")
	writeFile(t, filepath.Join(wc, name), []byte("changed\n"))
	run(t, wc, "svn", "changelist", "feature", svn.LiteralTarget(name))
	manager := testManager(t)
	manager.root = filepath.Join(filepath.Dir(manager.root), "snapshots@state")
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if entry := entriesByPath(manifest.Entries)[name]; entry.Changelist != "feature" || entry.Payload == "" {
		t.Fatalf("entry = %+v", entry)
	}
	run(t, wc, "svn", "revert", svn.LiteralTarget(name))
	run(t, wc, "svn", "changelist", "--remove", svn.LiteralTarget(name))
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(wc, name), []byte("changed\n"))
	statuses, err := manager.readStatus(t.Context(), wc)
	if err != nil {
		t.Fatal(err)
	}
	if statusByPath(statuses)[name].Changelist != "feature" {
		t.Fatalf("status = %+v", statuses)
	}
}

func TestRestoreRejectsPatchWithRejectedHunks(t *testing.T) {
	wc := newWorkingCopy(t)
	writeFile(t, filepath.Join(wc, "text.txt"), []byte("snapshot\n"))
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	patch := "Index: text.txt\n===================================================================\n--- text.txt\t(revision 2)\n+++ text.txt\t(working copy)\n@@ -1 +1 @@\n-this line never existed\n+snapshot\n"
	writeFile(t, filepath.Join(manager.Root(), manifest.ID, manifest.Patch), []byte(patch))
	run(t, wc, "svn", "revert", "text.txt")
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err == nil {
		t.Fatal("restore accepted rejected hunks")
	}
	assertFile(t, filepath.Join(wc, "text.txt"), []byte("base\n"))
}

func TestRestoreKeepsUnrelatedUnversionedFiles(t *testing.T) {
	wc := newWorkingCopy(t)
	writeFile(t, filepath.Join(wc, "text.txt"), []byte("snapshot\n"))
	manager := testManager(t)
	manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run(t, wc, "svn", "revert", "text.txt")
	writeFile(t, filepath.Join(wc, "notes", "keep.txt"), []byte("keep\n"))
	if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(wc, "notes", "keep.txt"), []byte("keep\n"))
}

func TestRestoreRejectsNewlyVersionedCollision(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "added", true: "unversioned"}[selected], func(t *testing.T) {
			wc := newWorkingCopy(t)
			writeFile(t, filepath.Join(wc, "later.txt"), []byte("snapshot\n"))
			options := CreateOptions{}
			if selected {
				options.IncludeUnversioned = []string{"later.txt"}
			} else {
				run(t, wc, "svn", "add", "later.txt")
			}
			manager := testManager(t)
			manifest, err := manager.Create(t.Context(), wc, options)
			if err != nil {
				t.Fatal(err)
			}
			if selected {
				run(t, wc, "svn", "add", "later.txt")
			}
			writeFile(t, filepath.Join(wc, "later.txt"), []byte("committed later\n"))
			run(t, wc, "svn", "commit", "-m", "later commit", "later.txt")
			if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); !errors.Is(err, ErrRevisionMismatch) {
				t.Fatalf("restore = %v, want revision rejection", err)
			}
			assertFile(t, filepath.Join(wc, "later.txt"), []byte("committed later\n"))
		})
	}
}

func TestValidateManifestRejectsIncompleteOlderFormat(t *testing.T) {
	manifest := Manifest{Version: manifestVersion - 1, RepositoryUUID: "uuid", RootURL: "file:///repository", URL: "file:///repository/trunk", BaseRevision: 2}
	info := svn.Info{UUID: manifest.RepositoryUUID, RootURL: manifest.RootURL, URL: manifest.URL, Revision: manifest.BaseRevision}
	validation := validateManifest(manifest, info)
	if validation.Valid || len(validation.Problems) != 1 || validation.Problems[0] != ProblemManifest {
		t.Fatalf("validation = %+v", validation)
	}
}

func TestRestoreReplacementChangesNodeKind(t *testing.T) {
	for _, oldDir := range []bool{false, true} {
		t.Run(map[bool]string{false: "file-to-directory", true: "directory-to-file"}[oldDir], func(t *testing.T) {
			wc := newWorkingCopy(t)
			path := "node"
			if oldDir {
				path += "/old.txt"
			}
			writeFile(t, filepath.Join(wc, path), []byte("old\n"))
			run(t, wc, "svn", "add", "node")
			run(t, wc, "svn", "propset", "test:property", "before", "node")
			run(t, wc, "svn", "commit", "-m", "old node")
			run(t, wc, "svn", "delete", "node")
			path = "node"
			if !oldDir {
				path += "/new.txt"
			}
			writeFile(t, filepath.Join(wc, path), []byte("replacement\n"))
			run(t, wc, "svn", "add", "node")
			run(t, wc, "svn", "propset", "test:property", "after", "node")
			manager := testManager(t)
			manifest, err := manager.Create(t.Context(), wc, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			run(t, wc, "svn", "revert", "--depth", "infinity", "--remove-added", ".")
			if _, err := manager.Restore(t.Context(), manifest.ID, wc, RestoreOptions{}); err != nil {
				t.Fatal(err)
			}
			assertFile(t, filepath.Join(wc, path), []byte("replacement\n"))
			if got := strings.TrimSpace(run(t, wc, "svn", "propget", "test:property", "node")); got != "after" {
				t.Fatalf("property = %q", got)
			}
		})
	}
}
