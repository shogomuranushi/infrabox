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

var sshSession string

var sshCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "Open a shell in a VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		name := args[0]
		autoUploadFlag, _ := cmd.Flags().GetBool("auto-upload")
		autoUpload := autoUploadFlag || cfg.AutoUploadPaste

		wsURL, err := buildExecURL(name, sshSession)
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

		// Stdin forwarder. If auto-upload is enabled, pipe stdin through the
		// paste interceptor; otherwise forward raw chunks unchanged.
		forwardToVM := func(chunk []byte) error {
			return writeMsg(websocket.BinaryMessage, chunk)
		}
		logf := func(format string, a ...interface{}) {
			// Go to column 0, print, then resume wherever the TUI was.
			fmt.Fprintf(os.Stderr, "\r\x1b[K"+format+"\r\n", a...)
		}
		var interceptor *pasteInterceptor
		if autoUpload {
			interceptor = newPasteInterceptor(name, forwardToVM, logf)
			fmt.Fprint(os.Stderr, "\r\x1b[33m[ib]\x1b[0m auto-upload-paste enabled — pasted local file paths will be confirmed & uploaded to the VM.\r\n")
		}

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
				if interceptor != nil {
					if ferr := interceptor.Feed(buf[:n]); ferr != nil {
						return
					}
				} else {
					if err := writeMsg(websocket.BinaryMessage, buf[:n]); err != nil {
						return
					}
				}
			}
		}()

		<-done
		if interceptor != nil {
			interceptor.Close()
		}
	},
}

func buildExecURL(name, session string) (string, error) {
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
	if session != "" {
		q := u.Query()
		q.Set("session", session)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func init() {
	sshCmd.Flags().StringVarP(&sshSession, "session", "s", "main", "tmux session name to attach to (created if it does not exist)")
	sshCmd.Flags().Bool("auto-upload", false, "Auto-upload local file paths pasted into the session to the VM (requires confirmation per file)")
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
