//go:build windows

package cmd

import "github.com/gorilla/websocket"

func watchResize(conn *websocket.Conn) {}
