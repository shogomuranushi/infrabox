package handlers

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

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

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ERROR: websocket upgrade for %s: %v", name, err)
		return
	}
	defer conn.Close()

	if err := h.k8s.ExecPod(r.Context(), vmNamespace, name, conn); err != nil {
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

// StorageInfo returns storage usage information for a VM.
// GET /v1/vms/{name}/storage?path=/home/ubuntu
func (h *Handler) StorageInfo(w http.ResponseWriter, r *http.Request) {
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

	// Get PVC capacity
	capacity, err := h.k8s.GetPVCInfo(r.Context(), vmNamespace, name)
	if err != nil {
		log.Printf("WARN: failed to get PVC info for %s: %v", name, err)
		capacity = "unknown"
	}

	// Get disk usage via df inside the pod
	dfOutput, err := h.k8s.ExecCommand(r.Context(), vmNamespace, name, []string{
		"df", "-B1", "--output=size,used,avail,pcent", "/home/ubuntu",
	})
	if err != nil {
		log.Printf("ERROR: df for %s: %v", name, err)
		jsonError(w, "failed to get storage info", http.StatusInternalServerError)
		return
	}

	usage := parseDF(dfOutput)
	usage["capacity"] = capacity

	// Optionally list files
	action := r.URL.Query().Get("action")
	if action == "ls" {
		lsPath := r.URL.Query().Get("path")
		if lsPath == "" {
			lsPath = "/home/ubuntu"
		}
		if !strings.HasPrefix(lsPath, "/") {
			jsonError(w, "path must be absolute", http.StatusBadRequest)
			return
		}
		lsOutput, err := h.k8s.ExecCommand(r.Context(), vmNamespace, name, []string{
			"ls", "-lah", lsPath,
		})
		if err != nil {
			log.Printf("ERROR: ls for %s: %v", name, err)
			jsonError(w, "failed to list files", http.StatusInternalServerError)
			return
		}
		usage["files"] = lsOutput
	}

	jsonOK(w, usage)
}

// parseDF parses the output of df -B1 --output=size,used,avail,pcent
func parseDF(output string) map[string]interface{} {
	result := map[string]interface{}{
		"total_bytes": int64(0),
		"used_bytes":  int64(0),
		"avail_bytes": int64(0),
		"use_percent": "0%",
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return result
	}

	// Parse the data line (second line)
	fields := strings.Fields(lines[1])
	if len(fields) >= 4 {
		if v, err := parseInt64(fields[0]); err == nil {
			result["total_bytes"] = v
		}
		if v, err := parseInt64(fields[1]); err == nil {
			result["used_bytes"] = v
		}
		if v, err := parseInt64(fields[2]); err == nil {
			result["avail_bytes"] = v
		}
		result["use_percent"] = strings.TrimSpace(fields[3])
	}

	return result
}

func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

// ResizeExec handles terminal resize requests.
// This is done via WebSocket binary messages in the exec connection itself,
// using the Kubernetes resize protocol (channel 4).
// No separate endpoint needed — resize messages are sent inline.

// WebSocket message protocol:
// - Text messages: stdin data
// - Binary messages with first byte = 0x04: resize JSON {"Width":cols,"Height":rows}
// - Server sends text messages: stdout/stderr data
