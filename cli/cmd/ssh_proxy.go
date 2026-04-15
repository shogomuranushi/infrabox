package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

var sshProxyLog *os.File

func initSSHProxyLog() {
	f, err := os.OpenFile("/tmp/ssh-proxy-debug.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err == nil {
		sshProxyLog = f
	}
}

func logSSHProxy(format string, args ...any) {
	msg := fmt.Sprintf("[ssh-proxy] "+format+"\n", args...)
	if sshProxyLog != nil {
		fmt.Fprint(sshProxyLog, msg)
	}
}

var sshProxyCmd = &cobra.Command{
	Use:   "ssh-proxy <name>",
	Short: "ProxyCommand bridge for SSH connections (used via ~/.ssh/config)",
	Long: `Bridges an SSH connection over WebSocket to a VM pod.
Acts as an SSH server on stdin/stdout — no sshd needed in the VM.

Add to ~/.ssh/config:

  Host infrabox-*
    User ubuntu
    ProxyCommand ib ssh-proxy %h

Then connect via Claude Code or ssh using "ubuntu@infrabox-<vmname>".
The "infrabox-" prefix is stripped to determine the VM name.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		initSSHProxyLog()
		mustConfig()
		name := strings.TrimPrefix(args[0], "infrabox-")
		logSSHProxy("started for VM: %s", name)
		if err := runSSHProxy(name); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			os.Exit(1)
		}
	},
}

func runSSHProxy(name string) error {
	hostKey, err := loadOrGenerateHostKey()
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}

	sshCfg := &ssh.ServerConfig{
		// Accept any public key — auth is handled by API key on the WebSocket side.
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	sshCfg.AddHostKey(hostKey)

	sshConn, chans, reqs, err := ssh.NewServerConn(newStdioConn(), sshCfg)
	if err != nil {
		return fmt.Errorf("SSH handshake: %w", err)
	}
	defer sshConn.Close()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, requests, err := newChan.Accept()
		if err != nil {
			return nil
		}
		dispatchSession(ch, requests, name)
		return nil
	}
	return nil
}

// dispatchSession waits for the first shell/exec request and routes accordingly:
//   - shell  → interactive tmux session over WebSocket
//   - exec   → run command via HTTP API, return output, close
func dispatchSession(ch ssh.Channel, requests <-chan *ssh.Request, vmName string) {
	defer ch.Close()

	var ptyCols, ptyRows uint32 = 80, 24

	for req := range requests {
		logSSHProxy("request type: %s", req.Type)
		switch req.Type {
		case "pty-req":
			var pty struct {
				Term    string
				Columns uint32
				Rows    uint32
				Width   uint32
				Height  uint32
				Modes   string
			}
			ssh.Unmarshal(req.Payload, &pty) //nolint:errcheck
			ptyCols, ptyRows = pty.Columns, pty.Rows
			logSSHProxy("pty-req: %dx%d term=%s", ptyCols, ptyRows, pty.Term)
			req.Reply(true, nil) //nolint:errcheck

		case "shell":
			logSSHProxy("shell → interactive")
			req.Reply(true, nil) //nolint:errcheck
			runInteractiveShell(ch, requests, vmName, ptyCols, ptyRows)
			return

		case "exec":
			var payload struct{ Command string }
			ssh.Unmarshal(req.Payload, &payload) //nolint:errcheck
			logSSHProxy("exec: %q", payload.Command)
			req.Reply(true, nil) //nolint:errcheck
			runExecCommand(ch, vmName, payload.Command)
			return

		default:
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

// runInteractiveShell bridges the SSH channel to a tmux session over WebSocket.
func runInteractiveShell(ch ssh.Channel, requests <-chan *ssh.Request, vmName string, cols, rows uint32) {
	wsURL, err := buildExecURL(vmName, "main")
	if err != nil {
		logSSHProxy("build URL error: %v", err)
		return
	}
	dialer := &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: false}}
	wsConn, _, err := dialer.Dial(wsURL, http.Header{"X-API-Key": {cfg.APIKey}})
	if err != nil {
		logSSHProxy("WebSocket error: %v", err)
		ch.Write([]byte(fmt.Sprintf("connection failed: %v\r\n", err))) //nolint:errcheck
		return
	}
	defer wsConn.Close()

	var wsMu sync.Mutex
	writeWS := func(msgType int, data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return wsConn.WriteMessage(msgType, data)
	}
	wsConn.SetPingHandler(func(d string) error {
		return writeWS(websocket.PongMessage, []byte(d))
	})

	sendWSResize(writeWS, cols, rows)

	// Handle window-change from ongoing requests
	go func() {
		for req := range requests {
			if req.Type == "window-change" {
				var wc struct{ Columns, Rows, Width, Height uint32 }
				ssh.Unmarshal(req.Payload, &wc) //nolint:errcheck
				sendWSResize(writeWS, wc.Columns, wc.Rows)
			}
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}()

	done := make(chan struct{})

	// WebSocket → SSH channel (VM stdout → client)
	go func() {
		defer close(done)
		for {
			_, msg, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			if _, err := ch.Write(msg); err != nil {
				return
			}
		}
	}()

	// SSH channel → WebSocket (client keystrokes → VM)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				writeWS(websocket.BinaryMessage, buf[:n]) //nolint:errcheck
			}
			if err != nil {
				writeWS(websocket.CloseMessage, //nolint:errcheck
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
		}
	}()

	<-done

	exitStatus := struct{ Status uint32 }{0}
	ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus)) //nolint:errcheck
}

// runExecCommand runs a command in the VM via the HTTP API and writes output to the SSH channel.
func runExecCommand(ch ssh.Channel, vmName, command string) {
	apiURL := strings.TrimRight(cfg.Endpoint, "/") + "/v1/vms/" + vmName + "/run"

	body, _ := json.Marshal(map[string]string{"command": command})
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		logSSHProxy("exec build request error: %v", err)
		sendExitStatus(ch, 1)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logSSHProxy("exec HTTP error: %v", err)
		ch.Write([]byte(err.Error() + "\n")) //nolint:errcheck
		sendExitStatus(ch, 1)
		return
	}
	defer resp.Body.Close()

	output, _ := io.ReadAll(resp.Body)
	logSSHProxy("exec output (%d bytes): %q", len(output), string(output))
	ch.Write(output) //nolint:errcheck

	exitCode, _ := strconv.Atoi(resp.Header.Get("X-Exit-Code"))
	sendExitStatus(ch, uint32(exitCode))
}

func sendExitStatus(ch ssh.Channel, code uint32) {
	exitStatus := struct{ Status uint32 }{code}
	ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus)) //nolint:errcheck
}

// sendWSResize sends a terminal resize message to the WebSocket (0x04 prefix + JSON).
func sendWSResize(writeWS func(int, []byte) error, cols, rows uint32) {
	size := struct {
		Width  uint16 `json:"Width"`
		Height uint16 `json:"Height"`
	}{uint16(cols), uint16(rows)}
	data, _ := json.Marshal(size)
	msg := make([]byte, 1+len(data))
	msg[0] = 0x04
	copy(msg[1:], data)
	writeWS(websocket.BinaryMessage, msg) //nolint:errcheck
}

// loadOrGenerateHostKey loads the SSH host key from ~/.ib/ssh_host_key,
// generating and saving a new Ed25519 key if it doesn't exist.
func loadOrGenerateHostKey() (ssh.Signer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".ib", "ssh_host_key")

	if data, err := os.ReadFile(path); err == nil {
		if signer, err := ssh.ParsePrivateKey(data); err == nil {
			return signer, nil
		}
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// stdioConn wraps os.Stdin/Stdout as a net.Conn for use with ssh.NewServerConn.
type stdioConn struct{}

func newStdioConn() net.Conn                               { return &stdioConn{} }
func (c *stdioConn) Read(b []byte) (int, error)            { return os.Stdin.Read(b) }
func (c *stdioConn) Write(b []byte) (int, error)           { return os.Stdout.Write(b) }
func (c *stdioConn) Close() error                          { return nil }
func (c *stdioConn) LocalAddr() net.Addr                   { return &net.UnixAddr{Name: "stdin"} }
func (c *stdioConn) RemoteAddr() net.Addr                  { return &net.UnixAddr{Name: "stdout"} }
func (c *stdioConn) SetDeadline(_ time.Time) error         { return nil }
func (c *stdioConn) SetReadDeadline(_ time.Time) error     { return nil }
func (c *stdioConn) SetWriteDeadline(_ time.Time) error    { return nil }

var _ io.ReadWriter = (*stdioConn)(nil)
