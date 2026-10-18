package config

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestAddProjectNormalizesRenamesAndRejectsDuplicateName(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	cfg := Config{}
	if err := cfg.AddProject(Project{Name: "  项目一  ", Path: filepath.Join(first, "sub", "..")}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0] != (Project{Name: "项目一", Path: first}) || !slices.Equal(cfg.Recent, []string{first}) {
		t.Fatalf("normalized project = %+v; recent = %v", cfg.Projects, cfg.Recent)
	}
	if err := cfg.AddProject(Project{Name: "项目二", Path: second}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddProject(Project{Name: "重命名", Path: first}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 || cfg.Projects[1].Name != "重命名" || !slices.Equal(cfg.Recent, []string{first, second}) {
		t.Fatalf("renamed project = %+v; recent = %v", cfg.Projects, cfg.Recent)
	}
	before := slices.Clone(cfg.Projects)
	if err := cfg.AddProject(Project{Name: "重命名", Path: filepath.Join(root, "third")}); err == nil {
		t.Fatal("duplicate project name accepted for a different working copy")
	}
	if !slices.Equal(cfg.Projects, before) {
		t.Fatal("rejected project altered existing projects")
	}
	for _, project := range []Project{{Name: "  ", Path: first}, {Name: "empty path", Path: "  "}} {
		if err := cfg.AddProject(project); err == nil {
			t.Fatalf("empty project fields accepted: %+v", project)
		}
	}
}

func TestProjectStoreUpdatePreservesOtherSettingsAndRejectedChanges(t *testing.T) {
	root := t.TempDir()
	store := Store{Path: filepath.Join(root, "config.json")}
	initial := Config{Editor: "editor", ExpertMode: true, Favorites: []Favorite{{Name: "repository", URL: "https://example.test/repository"}}}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	// 每次更新重读最新配置，两次独立项目注册都必须保留。
	for _, name := range []string{"first", "second"} {
		if err := store.Update(func(cfg *Config) error {
			return cfg.AddProject(Project{Name: name, Path: filepath.Join(root, name)})
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Projects) != 2 || before.Editor != initial.Editor || !before.ExpertMode || !slices.Equal(before.Favorites, initial.Favorites) {
		t.Fatalf("unrelated settings or projects were lost: %+v", before)
	}
	rejected := errors.New("reject edit")
	if err := store.Update(func(cfg *Config) error {
		cfg.Editor = "should not persist"
		cfg.Projects = nil
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatalf("Update error = %v", err)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected update persisted: before=%+v after=%+v", before, after)
	}
}
