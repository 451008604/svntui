package service

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"svntui/internal/config"
	"svntui/internal/domain"
)

type ProjectSummary struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Modified    int    `json:"modified"`
	Conflicts   int    `json:"conflicts"`
	Unversioned int    `json:"unversioned"`
	Error       string `json:"error,omitempty"`
}

func (m Modules) currentConfig() (config.Config, error) {
	if m.ConfigStore.Path != "" {
		return m.ConfigStore.Load()
	}
	return m.Config, nil
}

func (m Modules) Projects(ctx context.Context) ([]ProjectSummary, error) {
	cfg, err := m.currentConfig()
	if err != nil {
		return nil, err
	}
	projects := slices.Clone(cfg.Projects)
	for _, path := range cfg.Recent {
		if !slices.ContainsFunc(projects, func(p config.Project) bool { return p.Path == path }) {
			projects = append(projects, config.Project{Name: filepath.Base(path), Path: path})
		}
	}
	result := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := ProjectSummary{Name: project.Name, Path: project.Path}
		entries, err := m.Client.Status(ctx, project.Path, false, false)
		if err != nil {
			row.Error = err.Error()
		} else {
			for _, entry := range entries {
				if entry.IsVersionedDirty() {
					row.Modified++
				}
				if entry.IsConflict() {
					row.Conflicts++
				}
				if entry.Item == "unversioned" {
					row.Unversioned++
				}
			}
		}
		result = append(result, row)
	}
	return result, nil
}

func (m Modules) projects(ctx context.Context) (domain.ModuleData, error) {
	projects, err := m.Projects(ctx)
	if err != nil {
		return domain.ModuleData{}, err
	}
	rows := make([]domain.ModuleRow, 0, len(projects))
	for _, p := range projects {
		state := fmt.Sprintf("修改 %d · 冲突 %d · 未跟踪 %d", p.Modified, p.Conflicts, p.Unversioned)
		if p.Error != "" {
			state = "不可用: " + p.Error
		}
		rows = append(rows, domain.ModuleRow{ID: p.Path, Target: p.Path, Kind: "project", Cells: []string{p.Name, state, p.Path}, Detail: state + "\n" + p.Path})
	}
	return domain.ModuleData{Title: "项目与最近工作副本", Columns: []string{"名称", "状态", "路径"}, Rows: rows, EmptyMessage: "暂无项目；使用 svntui project add --name 名称 路径"}, nil
}

func (m Modules) SwitchProject(ctx context.Context, path string) (string, error) {
	info, err := m.Client.Info(ctx, path)
	if err != nil {
		return "", err
	}
	root := info.WCAbsolute
	if root == "" {
		return "", fmt.Errorf("%s 不是工作副本", path)
	}
	if m.ConfigStore.Path != "" {
		if err := m.ConfigStore.Update(func(cfg *config.Config) error { cfg.RememberProject(root); return nil }); err != nil {
			return "", err
		}
	}
	return root, nil
}
