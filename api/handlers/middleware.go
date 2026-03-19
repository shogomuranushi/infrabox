package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"

	"github.com/shogomuranushi/infra-box/api/db"
)

type contextKey string

const ctxUser contextKey = "user"

func APIKeyMiddleware(adminKey string, database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if key == "" {
				jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Admin key: full access, no owner filter (constant-time compare)
			if subtle.ConstantTimeCompare([]byte(key), []byte(adminKey)) == 1 {
				ctx := context.WithValue(r.Context(), ctxUser, "")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// User key: hash the provided key and look up by hash
			h := sha256.Sum256([]byte(key))
			hashed := hex.EncodeToString(h[:])
			k, err := database.FindKeyByValue(hashed)
			if err != nil || k == nil {
				jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxUser, k.Name)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// currentUser returns the user name from context ("" = admin).
func currentUser(r *http.Request) string {
	v, _ := r.Context().Value(ctxUser).(string)
	return v
}
