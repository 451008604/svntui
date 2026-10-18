package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadJSON loads a JSON document. A missing file is treated as an empty document.
func LoadJSON[T any](path string, dst *T) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("解码 %s: %w", path, err)
	}
	return nil
}

// SaveJSON atomically replaces a JSON document and restricts it to the current user.
func SaveJSON[T any](path string, value T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建存储目录: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 %s: %w", path, err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".svntui-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时存储文件: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("保护临时存储文件: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("写入临时存储文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步临时存储文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时存储文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	ok = true
	return nil
}
