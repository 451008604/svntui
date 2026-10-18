package config

import (
	"path/filepath"
	"testing"
)

func TestRememberProjectDeduplicatesAndMovesToFront(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	cfg := Config{Recent: []string{a, b}}
	cfg.RememberProject(b)
	if len(cfg.Recent) != 2 || cfg.Recent[0] != b || cfg.Recent[1] != a {
		t.Fatalf("unexpected recent projects: %v", cfg.Recent)
	}
}

func TestAddFavoriteReplacesURL(t *testing.T) {
	cfg := Config{Favorites: []Favorite{{Name: "old", URL: "https://example.test/a"}}}
	cfg.AddFavorite(Favorite{Name: "new", URL: "https://example.test/a"})
	if len(cfg.Favorites) != 1 || cfg.Favorites[0].Name != "new" {
		t.Fatalf("unexpected favorites: %+v", cfg.Favorites)
	}
}
