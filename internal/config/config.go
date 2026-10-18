package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/appdirs"
	"svntui/internal/storage"
)

const currentVersion = 1

// Favorite is a user-defined repository path label. It carries no branch semantics.
type Favorite struct {
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	URL   string `json:"url"`
}

// Config contains global, non-secret UI preferences.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Config struct {
	Projects     []Project  `json:"projects,omitempty"`
	Version      int        `json:"version"`
	Recent       []string   `json:"recent,omitempty"`
	Favorites    []Favorite `json:"favorites,omitempty"`
	ExpertMode   bool       `json:"expert_mode,omitzero"`
	Editor       string     `json:"editor,omitempty"`
	MergeTool    string     `json:"merge_tool,omitempty"`
	IgnoreColors bool       `json:"ignore_colors,omitzero"`
}

// Store persists global configuration.
type Store struct {
	Path string
}

func NewStore(dirs appdirs.Dirs) Store {
	return Store{Path: filepath.Join(dirs.Config, "config.json")}
}

func (s Store) Load() (Config, error) {
	cfg := Config{Version: currentVersion}
	if err := storage.LoadJSON(s.Path, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Version == 0 {
		cfg.Version = currentVersion
	}
	if cfg.Version != currentVersion {
		return Config{}, errors.New("不支持的配置版本")
	}
	return cfg, nil
}

func (s Store) Save(cfg Config) error {
	cfg.Version = currentVersion
	return storage.SaveJSON(s.Path, cfg)
}

// RememberProject moves a working copy to the front of the recent list.
func (c *Config) RememberProject(path string) {
	path = filepath.Clean(path)
	c.Recent = slices.DeleteFunc(c.Recent, func(item string) bool {
		return filepath.Clean(item) == path
	})
	c.Recent = append([]string{path}, c.Recent...)
	if len(c.Recent) > 20 {
		c.Recent = c.Recent[:20]
	}
}

func (c *Config) AddFavorite(f Favorite) {
	c.Favorites = slices.DeleteFunc(c.Favorites, func(item Favorite) bool {
		return item.URL == f.URL
	})
	c.Favorites = append(c.Favorites, f)
}

func (c *Config) AddProject(project Project) error {
	project.Name = strings.TrimSpace(project.Name)
	if project.Name == "" || strings.TrimSpace(project.Path) == "" {
		return errors.New("项目名称和路径不能为空")
	}
	absolute, err := filepath.Abs(project.Path)
	if err != nil {
		return err
	}
	project.Path = absolute
	for _, existing := range c.Projects {
		if existing.Name == project.Name && existing.Path != project.Path {
			return fmt.Errorf("项目名称 %q 已被使用", project.Name)
		}
	}
	c.Projects = slices.DeleteFunc(c.Projects, func(p Project) bool { return p.Path == project.Path })
	c.Projects = append(c.Projects, project)
	c.RememberProject(project.Path)
	return nil
}
