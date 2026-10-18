//go:build !windows

package tui

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func watchTerminalResize(ctx context.Context, _ terminal) (<-chan struct{}, func()) {
	signals := make(chan os.Signal, 1)
	events := make(chan struct{}, 1)
	watchCtx, cancel := context.WithCancel(ctx)
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-signals:
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	return events, func() {
		cancel()
		signal.Stop(signals)
	}
}
