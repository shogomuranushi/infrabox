package cmd

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/spf13/cobra"
)

var forwardReverse []string

var forwardCmd = &cobra.Command{
	Use:   "forward <vm>",
	Short: "Forward TCP ports between the local machine and a VM",
	Long: `Forward TCP ports between the local machine and a VM over the InfraBox tunnel.

Currently supported: reverse forwarding (-R), which exposes a port from your
local machine to the inside of the VM. A process running inside the VM can
connect to 127.0.0.1:<vm-port> and be routed to 127.0.0.1:<local-port> on
your machine.

Example:
  # Expose local Chrome DevTools MCP (running on 127.0.0.1:9999) to the VM.
  ib forward myvm -R 9999:9999

Multiple -R flags are allowed, each opens a separate tunnel.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		vm := args[0]
		if len(forwardReverse) == 0 {
			fmt.Fprintln(os.Stderr, "ERROR: at least one -R <vm-port>:<local-port> is required")
			os.Exit(1)
		}

		errc := make(chan error, len(forwardReverse))
		for _, spec := range forwardReverse {
			vmPort, localPort, err := parseForwardSpec(spec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: invalid -R %q: %v\n", spec, err)
				os.Exit(1)
			}
			go func(vmPort, localPort int) {
				errc <- runReverseTunnel(vm, vmPort, localPort)
			}(vmPort, localPort)
		}

		// Wait for Ctrl+C or a tunnel error.
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		select {
		case <-sig:
			fmt.Fprintln(os.Stderr, "\ntunnel stopped")
		case err := <-errc:
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(1)
			}
		}
	},
}

// parseForwardSpec parses a string like "9999:9999" into (vmPort, localPort).
// Both ports must be 1024..65535.
func parseForwardSpec(s string) (vmPort, localPort int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("must be <vm-port>:<local-port>")
	}
	vmPort, err = strconv.Atoi(parts[0])
	if err != nil || vmPort < 1024 || vmPort > 65535 {
		return 0, 0, fmt.Errorf("vm-port must be an integer in 1024..65535")
	}
	localPort, err = strconv.Atoi(parts[1])
	if err != nil || localPort < 1 || localPort > 65535 {
		return 0, 0, fmt.Errorf("local-port must be an integer in 1..65535")
	}
	return vmPort, localPort, nil
}

// runReverseTunnel opens one tunnel and blocks until it closes.
func runReverseTunnel(vm string, vmPort, localPort int) error {
	wsURL, err := buildTunnelURL(vm, vmPort)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
	header := http.Header{}
	header.Set("X-API-Key", cfg.APIKey)

	conn, _, err := dialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer conn.Close()

	// Wrap the WebSocket in an io.ReadWriteCloser for yamux.
	wsRW := newWSConn(conn)

	// The API runs yamux.Server on this same connection; we are the client.
	session, err := yamux.Client(wsRW, yamuxConfig())
	if err != nil {
		return fmt.Errorf("yamux client: %w", err)
	}
	defer session.Close()

	fmt.Fprintf(os.Stderr, "Reverse tunnel active: vm:%d -> 127.0.0.1:%d (Ctrl+C to stop)\n", vmPort, localPort)

	// Accept streams opened by the API (one per pod-internal connection),
	// dial localhost:localPort for each, and copy bytes both ways.
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return fmt.Errorf("session closed: %w", err)
		}
		go handleReverseStream(stream, localPort)
	}
}

func handleReverseStream(stream *yamux.Stream, localPort int) {
	defer stream.Close()
	// Security: hard-coded 127.0.0.1 bind. Never use ":<port>" which would
	// listen on all interfaces including Docker/WSL2 bridges.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	local, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forward: dial %s: %v\n", addr, err)
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(local, stream); done <- struct{}{} }()
	go func() { io.Copy(stream, local); done <- struct{}{} }()
	<-done
}

func buildTunnelURL(vmName string, vmPort int) (string, error) {
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
	u.Path = fmt.Sprintf("/v1/vms/%s/tunnel", vmName)
	q := u.Query()
	q.Set("port", strconv.Itoa(vmPort))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// yamuxConfig mirrors the API-side config so both ends agree on framing.
func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	return c
}

// newWSConn adapts a gorilla websocket.Conn to io.ReadWriteCloser for yamux.
// Only BinaryMessage frames carry yamux bytes; other frame types are skipped.
func newWSConn(conn *websocket.Conn) *wsConn {
	return &wsConn{conn: conn}
}

type wsConn struct {
	conn   *websocket.Conn
	reader io.Reader
}

func (w *wsConn) Read(p []byte) (int, error) {
	for {
		if w.reader != nil {
			n, err := w.reader.Read(p)
			if err == io.EOF {
				w.reader = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		mt, r, err := w.conn.NextReader()
		if err != nil {
			return 0, err
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		w.reader = r
	}
}

func (w *wsConn) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsConn) Close() error { return w.conn.Close() }

func init() {
	forwardCmd.Flags().StringArrayVarP(&forwardReverse, "reverse", "R", nil,
		"Reverse forward: vm-port:local-port (may be repeated)")
}
