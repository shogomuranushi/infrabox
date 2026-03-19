package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shogomuranushi/infra-box/api/db"
)

type CreateKeyRequest struct {
	Name string `json:"name"`
}

// CreateKey handles POST /v1/keys.
//
// Auth modes:
//   - Google auth (oauth2-proxy): nginx-ingress forwards X-Auth-Request-Email.
//     The email is used as the key name. Idempotent: returns existing key if already issued.
//   - Open mode (no X-Auth-Request-Email): name comes from the JSON request body.
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.Header.Get("X-Auth-Request-Email"))

	if name == "" {
		// Fall back to request body (open mode)
		var req CreateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		name = strings.TrimSpace(req.Name)
	}

	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// Idempotent: return existing key if already issued for this name
	existing, err := h.db.FindKeyByName(name)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		jsonOK(w, map[string]string{"api_key": existing.APIKey, "name": existing.Name})
		return
	}

	k := &db.Key{
		ID:        uuid.NewString(),
		Name:      name,
		APIKey:    uuid.NewString(),
		CreatedAt: time.Now(),
	}
	if err := h.db.InsertKey(k); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"api_key": k.APIKey, "name": k.Name})
}
