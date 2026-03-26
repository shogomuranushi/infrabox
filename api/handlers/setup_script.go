package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/google/uuid"
)

const maxSetupScriptSize = 64 * 1024 // 64 KB

type SetupScriptRequest struct {
	Script string `json:"script"`
}

type SetupScriptResponse struct {
	Script string `json:"script"`
}

// SaveSetupScript handles PUT /v1/setup-script
func (h *Handler) SaveSetupScript(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" {
		jsonError(w, "setup scripts require a user key (not admin key)", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxSetupScriptSize+1))
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req SetupScriptRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Script == "" {
		jsonError(w, "script must not be empty", http.StatusBadRequest)
		return
	}
	if len(req.Script) > maxSetupScriptSize {
		jsonError(w, "script too large (max 64KB)", http.StatusRequestEntityTooLarge)
		return
	}

	if err := h.db.SaveSetupScript(uuid.NewString(), user, []byte(req.Script)); err != nil {
		log.Printf("ERROR saving setup script for %s: %v", user, err)
		jsonError(w, "failed to save setup script", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "saved"})
}

// GetSetupScript handles GET /v1/setup-script
func (h *Handler) GetSetupScript(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" {
		jsonError(w, "setup scripts require a user key (not admin key)", http.StatusBadRequest)
		return
	}

	script, err := h.db.GetSetupScript(user)
	if err != nil {
		log.Printf("ERROR getting setup script for %s: %v", user, err)
		jsonError(w, "failed to get setup script", http.StatusInternalServerError)
		return
	}
	if script == nil {
		jsonError(w, "no setup script configured", http.StatusNotFound)
		return
	}

	jsonOK(w, SetupScriptResponse{Script: string(script)})
}

// DeleteSetupScript handles DELETE /v1/setup-script
func (h *Handler) DeleteSetupScript(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == "" {
		jsonError(w, "setup scripts require a user key (not admin key)", http.StatusBadRequest)
		return
	}

	if err := h.db.DeleteSetupScript(user); err != nil {
		log.Printf("ERROR deleting setup script for %s: %v", user, err)
		jsonError(w, "failed to delete setup script", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
