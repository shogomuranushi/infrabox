package k8s

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// TunnelPod bridges a reverse TCP tunnel between a WebSocket client (ib CLI)
// and the infrabox-agent binary running inside the VM pod.
//
// Topology:
//
//	CLI  <--yamux-over-WebSocket-->  API  <--yamux-over-SPDY-exec-->  agent (in pod)
//
// The agent listens on 127.0.0.1:vmPort inside the pod and opens a new yamux
// stream per accepted connection. The API accepts those streams and proxies
// each to a corresponding stream opened over the CLI's WebSocket, where the
// CLI dials its own localhost port and copies bytes bidirectionally.
//
// The WebSocket must already be upgraded and authenticated by the caller.
// The caller is also responsible for validating vmPort (1..65535).
func (c *Client) TunnelPod(ctx context.Context, namespace, vmName string, vmPort int, conn *websocket.Conn) error {
	podName, err := c.findVMPod(ctx, namespace, vmName)
	if err != nil {
		return err
	}

	// Kick off the agent inside the pod. The port argument comes from the
	// caller but has already been range-checked; we stringify it locally so
	// there is no shell interpolation path.
	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "vm",
			Command: []string{
				"/usr/local/bin/infrabox-agent",
				"--port", strconv.Itoa(vmPort),
			},
			Stdin:  true,
			Stdout: true,
			Stderr: true,
			TTY:    false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}

	// Pipes bridging the executor's stdin/stdout to a yamux session on the
	// API side. Because remotecommand.StreamWithContext writes stdout to an
	// io.Writer and reads stdin from an io.Reader, we bolt io.Pipes onto
	// either side so we can expose a single io.ReadWriteCloser to yamux.
	execStdin, apiWriter := io.Pipe()   // exec reads what API writes
	apiReader, execStdout := io.Pipe()  // exec writes what API reads

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	// Run the exec. Its lifetime is the lifetime of the tunnel.
	go func() {
		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:  execStdin,
			Stdout: execStdout,
			Stderr: execStderrLogger{vmName: vmName},
		})
		apiWriter.Close()
		execStdout.Close()
		errc <- err
	}()

	// Agent-side yamux server: the API accepts streams opened by the agent
	// (one per pod-internal TCP connection).
	agentSide, err := yamux.Server(agentStdio{r: apiReader, w: apiWriter}, yamuxConfig())
	if err != nil {
		cancel()
		return fmt.Errorf("yamux server (agent): %w", err)
	}
	defer agentSide.Close()

	// CLI-side yamux server over the WebSocket. The CLI acts as the yamux
	// client and opens streams it wants to proxy back to the laptop.
	wsRW := newWSConn(conn)
	cliSide, err := yamux.Server(wsRW, yamuxConfig())
	if err != nil {
		cancel()
		return fmt.Errorf("yamux server (cli): %w", err)
	}
	defer cliSide.Close()

	// Accept streams from the agent (reverse direction) and pair each with a
	// freshly opened stream on the CLI side.
	go func() {
		for {
			s, err := agentSide.AcceptStream()
			if err != nil {
				errc <- fmt.Errorf("agent accept: %w", err)
				return
			}
			go func(agentStream *yamux.Stream) {
				defer agentStream.Close()
				cliStream, err := cliSide.OpenStream()
				if err != nil {
					log.Printf("tunnel %s: cli open: %v", vmName, err)
					return
				}
				defer cliStream.Close()
				proxyStreams(agentStream, cliStream)
			}(s)
		}
	}()

	// Block until exec dies, yamux errors out, or the caller cancels ctx.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

// proxyStreams copies bytes bidirectionally between two yamux streams until
// either side closes. It returns when both directions have finished.
func proxyStreams(a, b io.ReadWriter) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// yamuxConfig returns the shared yamux configuration used by both sides.
// Keepalives are enabled so dead sessions are detected without depending on
// the transport layer alone.
func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}

// agentStdio bundles the exec's pipe ends into an io.ReadWriteCloser suitable
// for yamux.
type agentStdio struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (a agentStdio) Read(p []byte) (int, error)  { return a.r.Read(p) }
func (a agentStdio) Write(p []byte) (int, error) { return a.w.Write(p) }
func (a agentStdio) Close() error {
	a.r.Close()
	a.w.Close()
	return nil
}

// execStderrLogger captures agent stderr and funnels it to the API log with a
// VM-name prefix so tunnel-side errors are visible to operators.
type execStderrLogger struct{ vmName string }

func (e execStderrLogger) Write(p []byte) (int, error) {
	log.Printf("tunnel %s agent: %s", e.vmName, string(p))
	return len(p), nil
}

// newWSConn wraps a gorilla websocket.Conn so it can be used as an
// io.ReadWriteCloser by yamux. Only BinaryMessage frames are exchanged; text
// and control frames are ignored on read.
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
			// Ignore pings, pongs, close, and accidental text frames.
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
