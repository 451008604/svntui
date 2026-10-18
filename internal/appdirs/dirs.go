package appdirs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Dirs 保存 svntui 在当前操作系统使用的配置、状态和缓存目录。
type Dirs struct {
	Config string
	State  string
	Cache  string
}

// Resolve 按当前系统约定返回应用目录，但不主动创建。
func Resolve() (Dirs, error) {
	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		var err error
		configBase, err = os.UserConfigDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("解析用户配置目录: %w", err)
		}
	}
	stateBase := os.Getenv("XDG_STATE_HOME")
	if stateBase == "" {
		if runtime.GOOS == "windows" {
			stateBase = os.Getenv("LOCALAPPDATA")
			if stateBase == "" {
				var err error
				stateBase, err = os.UserCacheDir()
				if err != nil {
					return Dirs{}, fmt.Errorf("解析用户状态目录: %w", err)
				}
			}
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return Dirs{}, fmt.Errorf("解析用户主目录: %w", err)
			}
			stateBase = filepath.Join(home, ".local", "state")
		}
	}
	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		var err error
		cacheBase, err = os.UserCacheDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("解析用户缓存目录: %w", err)
		}
	}

	return Dirs{
		Config: filepath.Join(configBase, "svntui"),
		State:  filepath.Join(stateBase, "svntui"),
		Cache:  filepath.Join(cacheBase, "svntui"),
	}, nil
}

// Ensure 创建应用目录；Unix 权限限制为当前用户，Windows 继承用户目录 ACL。
func (d Dirs) Ensure() error {
	for _, dir := range []string{d.Config, d.State, d.Cache} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建目录 %s: %w", dir, err)
		}
	}
	return nil
}
