//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/gorilla/websocket"
)

func watchResize(conn *websocket.Conn) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	go func() {
		for range sigCh {
			sendTermSize(conn)
		}
	}()
}
