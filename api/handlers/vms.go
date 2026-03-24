package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shogomuranushi/infra-box/api/config"
	"github.com/shogomuranushi/infra-box/api/db"
	k8sclient "github.com/shogomuranushi/infra-box/api/k8s"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

type Handler struct {
	cfg *config.Config
	db  *db.DB
	k8s *k8sclient.Client
}

func NewHandler(cfg *config.Config, database *db.DB, k8s *k8sclient.Client) *Handler {
	return &Handler{cfg: cfg, db: database, k8s: k8s}
}

type VMResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	AuthEnabled bool   `json:"auth_enabled"`
	ExecURL     string `json:"exec_url"`
	IngressURL  string `json:"ingress_url"`
	CreatedAt   string `json:"created_at"`
}

type CreateVMRequest struct {
	Name string `json:"name"`
}

func (h *Handler) CreateVM(w http.ResponseWriter, r *http.Request) {
	var req CreateVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validName.MatchString(req.Name) {
		jsonError(w, "name must match ^[a-z][a-z0-9-]{0,30}$", http.StatusBadRequest)
		return
	}

	user := currentUser(r)

	userVMs, err := h.db.ListVMs(user)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if len(userVMs) >= h.cfg.MaxVMsPerUser {
		jsonError(w, fmt.Sprintf("VM quota exceeded: you have %d/%d VMs. Delete an existing VM before creating a new one.", len(userVMs), h.cfg.MaxVMsPerUser), http.StatusTooManyRequests)
		return
	}

	existing, err := h.db.GetVM(req.Name, "")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		jsonError(w, "VM name already exists", http.StatusConflict)
		return
	}

	vmNamespace := k8sclient.UserNamespace(h.cfg.VMNamespace, user)

	// Ensure per-user namespace and ResourceQuota exist
	if user != "" {
		if err := h.k8s.EnsureUserNamespace(r.Context(), vmNamespace, h.cfg.UserCPUQuota, h.cfg.UserMemoryQuota); err != nil {
			log.Printf("ERROR ensuring namespace %s: %v", vmNamespace, err)
			jsonError(w, "failed to prepare VM infrastructure", http.StatusInternalServerError)
			return
		}
	}

	vm := &db.VM{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Owner:       user,
		Namespace:   vmNamespace,
		State:       "creating",
		AuthEnabled: true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := h.db.InsertVM(vm); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	ingressHost := h.ingressHost(req.Name)
	k8sCfg := k8sclient.VMConfig{
		Name:                    req.Name,
		Namespace:               vmNamespace,
		StorageClass:            h.cfg.StorageClass,
		BaseImage:               h.cfg.BaseImage,
		IngressClass:            h.cfg.IngressClass,
		IngressHost:             ingressHost,
		AuthURL:                 h.cfg.AuthURL,
		AuthEnabled:             true,
		Owner:                   user,
		NodeSelector:            h.cfg.VMNodeSelector,
		RcloneDriveClientID:     h.cfg.RcloneDriveClientID,
		RcloneDriveClientSecret: h.cfg.RcloneDriveClientSecret,
	}

	if err := h.k8s.CreateVM(r.Context(), k8sCfg); err != nil {
		log.Printf("ERROR creating VM %s: %v", req.Name, err)
		h.db.UpdateVMState(req.Name, "error")
		jsonError(w, "failed to create VM", http.StatusInternalServerError)
		return
	}

	// Wait for Pod to be ready (60s timeout)
	podReady := true
	if err := h.k8s.WaitForPodReady(r.Context(), vmNamespace, req.Name, 60); err != nil {
		log.Printf("WARN: pod not ready for %s: %v", req.Name, err)
		podReady = false
	}

	state := "running"
	if !podReady {
		state = "starting"
	}
	h.db.UpdateVMState(req.Name, state)
	vm.State = state

	jsonOK(w, h.toResponse(vm, ingressHost))
}

func (h *Handler) ListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := h.db.ListVMs(currentUser(r))
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	result := make([]VMResponse, 0, len(vms))
	for _, vm := range vms {
		result = append(result, h.toResponse(vm, h.ingressHost(vm.Name)))
	}
	jsonOK(w, map[string]interface{}{"vms": result})
}

func (h *Handler) GetVM(w http.ResponseWriter, r *http.Request) {
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
	jsonOK(w, h.toResponse(vm, h.ingressHost(vm.Name)))
}

func (h *Handler) DeleteVM(w http.ResponseWriter, r *http.Request) {
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
		vmNamespace = h.cfg.VMNamespace // fallback for pre-migration VMs
	}
	if err := h.k8s.DeleteVM(r.Context(), vmNamespace, name); err != nil {
		log.Printf("WARN: DeleteVM k8s error for %s: %v", name, err)
	}
	h.db.DeleteVM(name, currentUser(r))

	w.WriteHeader(http.StatusNoContent)
}

type RenameVMRequest struct {
	Name string `json:"name"`
}

func (h *Handler) RenameVM(w http.ResponseWriter, r *http.Request) {
	oldName := chi.URLParam(r, "name")
	var req RenameVMRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validName.MatchString(req.Name) {
		jsonError(w, "name must match ^[a-z][a-z0-9-]{0,30}$", http.StatusBadRequest)
		return
	}

	user := currentUser(r)

	vm, err := h.db.GetVM(oldName, user)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if vm == nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	// Check new name is not already taken
	existing, err := h.db.GetVM(req.Name, "")
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		jsonError(w, "VM name already exists", http.StatusConflict)
		return
	}

	if err := h.db.RenameVM(oldName, req.Name, user); err != nil {
		jsonError(w, fmt.Sprintf("rename failed: %v", err), http.StatusInternalServerError)
		return
	}

	vm.Name = req.Name
	jsonOK(w, h.toResponse(vm, h.ingressHost(vm.Name)))
}

func (h *Handler) RestartVM(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	vm, err := h.db.GetVM(name, currentUser(r))
	if err != nil || vm == nil {
		jsonError(w, "VM not found", http.StatusNotFound)
		return
	}

	vmNamespace := vm.Namespace
	if vmNamespace == "" {
		vmNamespace = h.cfg.VMNamespace
	}
	if err := h.k8s.RestartVM(r.Context(), vmNamespace, name); err != nil {
		jsonError(w, fmt.Sprintf("restart failed: %v", err), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "restarting"})
}

func (h *Handler) HealthZ(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) ingressHost(name string) string {
	if h.cfg.IngressDomain != "" {
		return fmt.Sprintf("%s.%s", name, h.cfg.IngressDomain)
	}
	if h.cfg.IngressIP != "" {
		return fmt.Sprintf("%s.%s.nip.io", name, h.cfg.IngressIP)
	}
	return name + ".infra.example.com"
}

func (h *Handler) toResponse(vm *db.VM, ingressHost string) VMResponse {
	return VMResponse{
		ID:          vm.ID,
		Name:        vm.Name,
		State:       vm.State,
		AuthEnabled: vm.AuthEnabled,
		ExecURL:     fmt.Sprintf("wss://%s/v1/vms/%s/exec", h.ingressAPIHost(), vm.Name),
		IngressURL:  "https://" + ingressHost,
		CreatedAt:   vm.CreatedAt.Format(time.RFC3339),
	}
}

type UpdateVMAuthRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) UpdateVMAuth(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req UpdateVMAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	user := currentUser(r)
	vm, err := h.db.GetVM(name, user)
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
	if err := h.k8s.UpdateVMAuth(r.Context(), vmNamespace, name, h.cfg.AuthURL, req.Enabled); err != nil {
		log.Printf("ERROR updating auth for VM %s: %v", name, err)
		jsonError(w, "failed to update VM auth", http.StatusInternalServerError)
		return
	}

	if err := h.db.UpdateVMAuth(name, user, req.Enabled); err != nil {
		log.Printf("ERROR updating auth in db for VM %s: %v", name, err)
		jsonError(w, "failed to update VM auth in db", http.StatusInternalServerError)
		return
	}

	vm.AuthEnabled = req.Enabled
	jsonOK(w, h.toResponse(vm, h.ingressHost(vm.Name)))
}

func (h *Handler) ingressAPIHost() string {
	if h.cfg.IngressDomain != "" {
		return "api." + h.cfg.IngressDomain
	}
	return "localhost:8080"
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
