package config

import (
	"errors"
	"path/filepath"
	"time"

	"svntui/internal/wclock"
)

// 在跨进程锁内重读配置，避免项目切换覆盖其他会话刚保存的设置。
func (s Store) Update(change func(*Config) error) error {
	var lock *wclock.Lock
	var err error
	deadline := time.Now().Add(time.Second)
	for {
		lock, err = wclock.Acquire(filepath.Join(filepath.Dir(s.Path), "locks"), s.Path, "更新配置")
		if err == nil {
			break
		}
		if !errors.Is(err, wclock.ErrBusy) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer lock.Release()
	cfg, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(&cfg); err != nil {
		return err
	}
	return s.Save(cfg)
}
