package cmd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "Open a shell in a VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]

		wsURL, err := buildExecURL(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}

		dialer := websocket.Dialer{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
		}
		header := http.Header{}
		header.Set("X-API-Key", cfg.APIKey)

		conn, _, err := dialer.Dial(wsURL, header)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: connection failed: %v\n", err)
			os.Exit(1)
		}
		defer conn.Close()

		// Mutex-protected write to prevent concurrent write panics.
		// gorilla/websocket requires that only one goroutine writes at a time.
		var writeMu sync.Mutex
		writeMsg := func(msgType int, data []byte) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return conn.WriteMessage(msgType, data)
		}

		// Override the default pong handler so it uses the shared mutex.
		conn.SetPingHandler(func(appData string) error {
			return writeMsg(websocket.PongMessage, []byte(appData))
		})

		// Put terminal in raw mode
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to set raw terminal: %v\n", err)
			os.Exit(1)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)

		// Send initial terminal size
		sendTermSize(writeMsg)

		// Handle terminal resize (platform-specific)
		watchResize(writeMsg)

		// Read from WebSocket → stdout
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				os.Stdout.Write(msg)
			}
		}()

		// Read from stdin → WebSocket
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					writeMsg(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					return
				}
				if err := writeMsg(websocket.TextMessage, buf[:n]); err != nil {
					return
				}
			}
		}()

		<-done
	},
}

func buildExecURL(name string) (string, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint: %w", err)
	}

	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		u.Scheme = "wss"
	}

	u.Path = fmt.Sprintf("/v1/vms/%s/exec", name)
	return u.String(), nil
}

func sendTermSize(write func(int, []byte) error) {
	w, h, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return
	}
	size := struct {
		Width  uint16 `json:"Width"`
		Height uint16 `json:"Height"`
	}{uint16(w), uint16(h)}
	data, _ := json.Marshal(size)

	// Prepend 0x04 channel byte for resize
	msg := make([]byte, 1+len(data))
	msg[0] = 0x04
	copy(msg[1:], data)
	write(websocket.BinaryMessage, msg) //nolint:errcheck
}
