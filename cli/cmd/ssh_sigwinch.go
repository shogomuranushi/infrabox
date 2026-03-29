//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

func watchResize(write func(int, []byte) error) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			sendTermSize(write)
		}
	}()
}
