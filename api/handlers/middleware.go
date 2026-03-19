package handlers

import (
	"context"
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
			// Admin key: full access, no owner filter
			if key == adminKey {
				ctx := context.WithValue(r.Context(), ctxUser, "")
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			// User key: scoped to the key owner
			k, err := database.FindKeyByValue(key)
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
