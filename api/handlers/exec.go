package handlers

import (
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// tmuxSessionNameRE restricts session names to a safe character set so they
// can be passed as a literal argument to tmux without risk of shell escaping
// issues. tmux itself forbids '.' and ':' in session names.
var tmuxSessionNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var upgrader = websocket.Upgrader{
	// CheckOrigin allows all origins because CLI clients don't send Origin headers.
	// Authentication is enforced via X-API-Key header in the middleware, which
	// browsers cannot set on cross-origin WebSocket connections.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ExecVM handles WebSocket-based shell access to a VM pod.
// GET /v1/vms/{name}/exec
func (h *Handler) ExecVM(w http.ResponseWriter, r *http.Request) {
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

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}

	session := r.URL.Query().Get("session")
	if session == "" {
		session = "main"
	}
	if !tmuxSessionNameRE.MatchString(session) {
		jsonError(w, "invalid session name: must match [a-zA-Z0-9_-]{1,64}", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR: websocket upgrade for %s: %v", name, err)
		return
	}
	defer conn.Close()

	if err := h.k8s.ExecPod(r.Context(), vmNamespace, name, session, conn); err != nil {
		log.Printf("ERROR: exec for %s: %v", name, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "session ended"))
	}
}

// UploadFile handles file upload to a VM pod via tar over exec.
// POST /v1/vms/{name}/files?path=/dest/dir
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
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

	destPath := filepath.Clean(r.URL.Query().Get("path"))
	if destPath == "" || destPath == "." {
		destPath = "/home/ubuntu"
	}
	if !strings.HasPrefix(destPath, "/") {
		jsonError(w, "path must be absolute", http.StatusBadRequest)
		return
	}

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}

	// Limit upload size to 1 GiB
	body := http.MaxBytesReader(w, r.Body, 1<<30)
	if err := h.k8s.CopyToPod(r.Context(), vmNamespace, name, destPath, body); err != nil {
		log.Printf("ERROR: upload to %s: %v", name, err)
		jsonError(w, "upload failed", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

// DownloadFile handles file download from a VM pod via tar over exec.
// GET /v1/vms/{name}/files?path=/src/path
func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request) {
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

	srcPath := filepath.Clean(r.URL.Query().Get("path"))
	if srcPath == "" || srcPath == "." {
		jsonError(w, "path query parameter is required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(srcPath, "/") {
		jsonError(w, "path must be absolute", http.StatusBadRequest)
		return
	}

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}

	w.Header().Set("Content-Type", "application/x-tar")
	if err := h.k8s.CopyFromPod(r.Context(), vmNamespace, name, srcPath, w); err != nil {
		log.Printf("ERROR: download from %s: %v", name, err)
		// Headers already sent, can't return JSON error
	}
}

// ResizeExec handles terminal resize requests.
// This is done via WebSocket binary messages in the exec connection itself,
// using the Kubernetes resize protocol (channel 4).
// No separate endpoint needed — resize messages are sent inline.

// WebSocket message protocol:
// - Text messages: stdin data
// - Binary messages with first byte = 0x04: resize JSON {"Width":cols,"Height":rows}
// - Server sends text messages: stdout/stderr data
