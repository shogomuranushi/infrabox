package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shogomuranushi/infra-box/api/db"
)

type InvitationCodeResponse struct {
	ID        string  `json:"id"`
	Code      string  `json:"code,omitempty"` // only on creation
	Used      bool    `json:"used"`
	UsedBy    string  `json:"used_by,omitempty"`
	CreatedAt string  `json:"created_at"`
	UsedAt    *string `json:"used_at,omitempty"`
}

// CreateInvitationCode generates a new invitation code (admin only).
// POST /v1/invitations
func (h *Handler) CreateInvitationCode(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != "" {
		jsonError(w, "admin only", http.StatusForbidden)
		return
	}

	rawCode, err := generateInvitationCode()
	if err != nil {
		jsonError(w, "failed to generate code", http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256([]byte(rawCode))
	hashedCode := hex.EncodeToString(hash[:])

	ic := &db.InvitationCode{
		ID:        uuid.NewString(),
		CodeHash:  hashedCode,
		CreatedAt: time.Now(),
	}
	if err := h.db.InsertInvitationCode(ic); err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	// Show-once: return raw code only at creation
	jsonOK(w, InvitationCodeResponse{
		ID:        ic.ID,
		Code:      rawCode,
		Used:      false,
		CreatedAt: ic.CreatedAt.Format(time.RFC3339),
	})
}

// ListInvitationCodes returns all invitation codes (admin only).
// GET /v1/invitations
func (h *Handler) ListInvitationCodes(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != "" {
		jsonError(w, "admin only", http.StatusForbidden)
		return
	}

	codes, err := h.db.ListInvitationCodes()
	if err != nil {
		jsonError(w, "db error", http.StatusInternalServerError)
		return
	}

	result := make([]InvitationCodeResponse, 0, len(codes))
	for _, ic := range codes {
		resp := InvitationCodeResponse{
			ID:        ic.ID,
			Used:      ic.Used,
			UsedBy:    ic.UsedBy,
			CreatedAt: ic.CreatedAt.Format(time.RFC3339),
		}
		if ic.UsedAt != nil {
			s := ic.UsedAt.Format(time.RFC3339)
			resp.UsedAt = &s
		}
		result = append(result, resp)
	}
	jsonOK(w, map[string]interface{}{"invitation_codes": result})
}

// generateInvitationCode creates a 16-byte (128-bit) hex-encoded invitation code.
func generateInvitationCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
