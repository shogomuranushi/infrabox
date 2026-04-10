package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	sshPingInterval = 30 * time.Second
	sshIdleTimeout  = 8 * time.Hour
)

// ExecPod opens an interactive shell in the VM pod and bridges it to a WebSocket connection.
func (c *Client) ExecPod(ctx context.Context, namespace, name string, conn *websocket.Conn) error {
	podName, err := c.findVMPod(ctx, namespace, name)
	if err != nil {
		return err
	}

	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "vm",
			Command:   []string{"/bin/zsh", "-l"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}

	ws := &wsStream{conn: conn}
	ws.lastActivity.Store(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Keepalive: send ping every 30s to prevent proxy timeouts.
	// Idle timeout: close the session after 8h of no stdin activity.
	go func() {
		ping := time.NewTicker(sshPingInterval)
		idle := time.NewTicker(time.Minute)
		defer ping.Stop()
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				if err := ws.sendPing(); err != nil {
					cancel()
					return
				}
			case <-idle.C:
				last := time.Unix(0, ws.lastActivity.Load())
				if time.Since(last) > sshIdleTimeout {
					ws.sendClose("idle timeout")
					cancel()
					return
				}
			}
		}
	}()

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             ws,
		Stdout:            ws,
		Stderr:            ws,
		Tty:               true,
		TerminalSizeQueue: ws,
	})
	return err
}

// CopyToPod copies a tar stream into a pod at the specified destination path.
func (c *Client) CopyToPod(ctx context.Context, namespace, name, destPath string, reader io.Reader) error {
	podName, err := c.findVMPod(ctx, namespace, name)
	if err != nil {
		return err
	}

	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "vm",
			Command:   []string{"bash", "-c", "mkdir -p -- \"$1\" && tar xf - -C \"$1\"", "--", destPath},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}

	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  reader,
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
}

// CopyFromPod copies a tar stream from a pod path to the writer.
func (c *Client) CopyFromPod(ctx context.Context, namespace, name, srcPath string, writer io.Writer) error {
	podName, err := c.findVMPod(ctx, namespace, name)
	if err != nil {
		return err
	}

	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "vm",
			Command:   []string{"tar", "cf", "-", "-C", "/", srcPath},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("create executor: %w", err)
	}

	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: writer,
		Stderr: io.Discard,
	})
}

// findVMPod returns the name of the first running pod for the given VM.
func (c *Client) findVMPod(ctx context.Context, namespace, name string) (string, error) {
	pods, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=vm-" + name,
	})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running pod found for VM %s", name)
}

// wsStream bridges a WebSocket connection to K8s remotecommand streams.
// It implements io.Reader, io.Writer, and remotecommand.TerminalSizeQueue.
type wsStream struct {
	conn         *websocket.Conn
	mu           sync.Mutex   // protects all writes to conn
	buf          []byte
	resize       chan *remotecommand.TerminalSize
	once         sync.Once
	lastActivity atomic.Int64 // unix nano, updated on each stdin keystroke
}

func (ws *wsStream) initResize() {
	ws.once.Do(func() {
		ws.resize = make(chan *remotecommand.TerminalSize, 4)
	})
}

// Read reads from the WebSocket (stdin from client).
// Text messages are stdin data.
// Binary messages with first byte 0x04 are resize events.
func (ws *wsStream) Read(p []byte) (int, error) {
	ws.initResize()
	for {
		if len(ws.buf) > 0 {
			n := copy(p, ws.buf)
			ws.buf = ws.buf[n:]
			return n, nil
		}

		msgType, data, err := ws.conn.ReadMessage()
		if err != nil {
			return 0, err
		}

		if msgType == websocket.BinaryMessage && len(data) > 0 && data[0] == 0x04 {
			// Resize message
			var size remotecommand.TerminalSize
			if err := json.Unmarshal(data[1:], &size); err == nil {
				select {
				case ws.resize <- &size:
				default:
				}
			}
			continue
		}

		// Stdin data — update last activity timestamp.
		ws.lastActivity.Store(time.Now().UnixNano())
		ws.buf = data
	}
}

// sendPing sends a WebSocket ping frame to keep the connection alive through proxies.
func (ws *wsStream) sendPing() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.conn.WriteMessage(websocket.PingMessage, nil)
}

// sendClose sends a WebSocket close frame with the given reason.
func (ws *wsStream) sendClose(reason string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason))
}

// Write sends data to the WebSocket (stdout/stderr to client).
func (ws *wsStream) Write(p []byte) (int, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if err := ws.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Next implements remotecommand.TerminalSizeQueue.
func (ws *wsStream) Next() *remotecommand.TerminalSize {
	ws.initResize()
	size, ok := <-ws.resize
	if !ok {
		return nil
	}
	return size
}
