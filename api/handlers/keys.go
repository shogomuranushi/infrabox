package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shogomuranushi/infra-box/api/db"
)

// generateAPIKey creates a cryptographically random API key (32 bytes / 256 bits).
func generateAPIKey() (raw string, hashed string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hashed = hex.EncodeToString(h[:])
	return raw, hashed, nil
}

type CreateKeyRequest struct {
	Name           string `json:"name"`
	InvitationCode string `json:"invitation_code"`
}

// CreateKey handles POST /v1/keys.
//
// Auth modes:
//   - Google auth (oauth2-proxy): nginx-ingress forwards X-Auth-Request-Email.
//     The email is used as the key name. Idempotent: returns existing key if already issued.
//   - Open mode (no X-Auth-Request-Email): name comes from the JSON request body.
//
// In open mode, a valid invitation code is required.
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.Header.Get("X-Auth-Request-Email"))

	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && name == "" {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if name == "" {
		name = strings.TrimSpace(req.Name)
	}

	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}

	// In open mode (no oauth2-proxy), require invitation code
	if r.Header.Get("X-Auth-Request-Email") == "" {
		code := strings.TrimSpace(req.InvitationCode)
		if code == "" {
			jsonError(w, "invitation_code is required", http.StatusBadRequest)
			return
		}
		if err := h.db.RedeemInvitationCode(code); err != nil {
			jsonError(w, "invalid or already used invitation code", http.StatusForbidden)
			return
		}
	}

	// Idempotent: return existing key if already issued for this name
	existing, err := h.db.FindKeyByName(name)
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		// Don't reveal whether a name exists or not — generic error
		jsonError(w, "key creation failed", http.StatusConflict)
		return
	}

	rawKey, hashedKey, err := generateAPIKey()
	if err != nil {
		jsonError(w, "key generation failed", http.StatusInternalServerError)
		return
	}

	k := &db.Key{
		ID:        uuid.NewString(),
		Name:      name,
		APIKey:    hashedKey, // store hash only
		CreatedAt: time.Now(),
	}
	if err := h.db.InsertKey(k); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	// Show-once: return the raw key only at creation time. It is never stored.
	jsonOK(w, map[string]string{"api_key": rawKey, "name": k.Name})
}
