package handlers

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	k8sclient "github.com/shogomuranushi/infra-box/api/k8s"
)

var upgrader = websocket.Upgrader{
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

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR: websocket upgrade for %s: %v", name, err)
		return
	}
	defer conn.Close()

	if err := h.k8s.ExecPod(r.Context(), vmNamespace, name, conn); err != nil {
		log.Printf("ERROR: exec for %s: %v", name, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
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

	destPath := r.URL.Query().Get("path")
	if destPath == "" {
		destPath = "/home/ubuntu"
	}

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}

	if err := h.k8s.CopyToPod(r.Context(), vmNamespace, name, destPath, r.Body); err != nil {
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

	srcPath := r.URL.Query().Get("path")
	if srcPath == "" {
		jsonError(w, "path query parameter is required", http.StatusBadRequest)
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
