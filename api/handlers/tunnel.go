package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// TunnelVM handles the reverse TCP tunnel WebSocket endpoint.
// GET /v1/vms/{name}/tunnel?port=<vm-side-port>
//
// The caller (ib forward) opens this WebSocket and multiplexes a yamux
// session over it. The API spawns infrabox-agent inside the VM pod to listen
// on 127.0.0.1:<port>, and forwards each accepted connection back to the CLI
// as a yamux stream.
//
// Concurrent tunnel slots are rate-limited per user and per VM. Both counters
// are checked and incremented atomically before the WebSocket upgrade; the
// slot is released when this function returns.
func (h *Handler) TunnelVM(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	vm, err := h.db.GetVM(name, currentUser(r))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if vm == nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	// Parse and validate the target VM port. Only unprivileged ports are
	// accepted: the agent runs as the ubuntu user and cannot bind 1-1023
	// anyway, but we enforce the bound explicitly for clarity.
	portStr := r.URL.Query().Get("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1024 || port > 65535 {
		jsonError(w, "port must be an integer in 1024..65535", http.StatusBadRequest)
		return
	}

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}

	// Acquire a concurrent tunnel slot before doing any real work. currentUser
	// is already established by the auth middleware. For admin-issued requests
	// the user string is empty; we scope admin tunnels under a shared bucket.
	user := currentUser(r)
	if user == "" {
		user = "_admin"
	}
	release, ok := h.tryAcquireTunnelSlot(user, vmNamespace, name)
	if !ok {
		jsonError(w, "concurrent tunnel limit reached", http.StatusTooManyRequests)
		return
	}
	defer release()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR: websocket upgrade for tunnel %s: %v", name, err)
		return
	}
	defer conn.Close()

	if err := h.k8s.TunnelPod(r.Context(), vmNamespace, name, port, conn); err != nil {
		log.Printf("ERROR: tunnel for %s: %v", name, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "tunnel ended"))
	}
}
