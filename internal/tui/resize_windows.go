//go:build windows

package tui

import (
	"context"
	"time"
)

func watchTerminalResize(ctx context.Context, term terminal) (<-chan struct{}, func()) {
	events := make(chan struct{}, 1)
	watchCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		lastWidth, lastHeight, _ := term.Size()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				width, height, err := term.Size()
				if err != nil || width == lastWidth && height == lastHeight {
					continue
				}
				lastWidth, lastHeight = width, height
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	return events, cancel
}
