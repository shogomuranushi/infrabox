// Package main implements infrabox-agent, a tiny tunnel endpoint that runs
// inside a VM pod. It is invoked by the InfraBox API via kubectl exec; its
// stdin/stdout become the transport for a yamux session.
//
// Topology for reverse port-forward (ib forward -R remotePort:localPort):
//
//	[laptop:localPort] <-- ib forward <-- API <-- (kubectl exec) --> [pod] infrabox-agent
//	                                                                          |
//	                                                                  listens on 127.0.0.1:remotePort
//
// When a process inside the pod connects to 127.0.0.1:remotePort, the agent
// opens a new yamux stream over stdin/stdout. The API accepts the stream and
// forwards it to the CLI over the tunnel's WebSocket (which itself is yamux-
// multiplexed). The CLI dials 127.0.0.1:localPort on the laptop for each
// stream and copies bytes bidirectionally.
//
// Security: the listener is hard-bound to 127.0.0.1 and cannot be changed via
// CLI flags. Only processes sharing the pod's network namespace can reach it.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"

	"github.com/hashicorp/yamux"
)

func main() {
	port := flag.Int("port", 0, "TCP port to listen on (bound to 127.0.0.1)")
	flag.Parse()

	if *port < 1 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "infrabox-agent: --port must be in 1..65535")
		os.Exit(2)
	}

	// Stderr is where the log/debug output goes; the API captures it for
	// diagnostics. Stdin/stdout must not be polluted — they carry yamux frames.
	log.SetOutput(os.Stderr)
	log.SetPrefix("infrabox-agent: ")
	log.SetFlags(0)

	// Establish the yamux session over stdio. The agent acts as the client
	// (openers of streams) because it is the side that accepts new inbound
	// connections on the listener and pushes them up to the API.
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = os.Stderr
	session, err := yamux.Client(stdioConn{}, cfg)
	if err != nil {
		log.Fatalf("yamux client: %v", err)
	}
	defer session.Close()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	defer ln.Close()
	log.Printf("listening on %s", addr)

	// Close the listener when the yamux session dies so accept() unblocks.
	go func() {
		<-session.CloseChan()
		ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if session.IsClosed() {
				return
			}
			log.Printf("accept: %v", err)
			return
		}
		go handleConn(session, c)
	}
}

func handleConn(session *yamux.Session, c net.Conn) {
	defer c.Close()
	stream, err := session.Open()
	if err != nil {
		log.Printf("yamux open: %v", err)
		return
	}
	defer stream.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(stream, c); done <- struct{}{} }()
	go func() { io.Copy(c, stream); done <- struct{}{} }()
	<-done
}

// stdioConn adapts os.Stdin/os.Stdout to the net.Conn-ish interface yamux
// needs (io.ReadWriteCloser with deadlines). Deadlines are no-ops because
// kubectl exec's SPDY streams do not support them natively.
type stdioConn struct{}

func (stdioConn) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioConn) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdioConn) Close() error {
	os.Stdin.Close()
	return os.Stdout.Close()
}
