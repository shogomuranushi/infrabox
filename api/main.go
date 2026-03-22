package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shogomuranushi/infra-box/api/config"
	"github.com/shogomuranushi/infra-box/api/db"
	"github.com/shogomuranushi/infra-box/api/handlers"
	k8sclient "github.com/shogomuranushi/infra-box/api/k8s"
)

func main() {
	cfg := config.Load()

	if cfg.APIKey == "" {
		log.Fatal("INFRABOX_API_KEY must be set")
	}

	// ensure data directory exists
	if err := os.MkdirAll("/data", 0755); err != nil {
		log.Printf("WARN: cannot create /data: %v (using current dir)", err)
		cfg.DBPath = "./infrabox.db"
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	log.Printf("DB opened: %s", cfg.DBPath)

	k8s, err := k8sclient.NewClient()
	if err != nil {
		log.Fatalf("failed to create k8s client: %v", err)
	}
	log.Printf("K8s client initialized")

	h := handlers.NewHandler(cfg, database, k8s)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Rate limiter for key creation: 5 requests/minute per IP, burst of 3
	keyRL := handlers.NewKeyRateLimiter()

	r.Get("/healthz", h.HealthZ)
	r.With(keyRL.Middleware).Post("/v1/keys", h.CreateKey)

	r.Group(func(r chi.Router) {
		r.Use(handlers.APIKeyMiddleware(cfg.APIKey, database))

		r.Post("/v1/vms", h.CreateVM)
		r.Get("/v1/vms", h.ListVMs)
		r.Get("/v1/vms/{name}", h.GetVM)
		r.Delete("/v1/vms/{name}", h.DeleteVM)
		r.Patch("/v1/vms/{name}", h.RenameVM)
		r.Post("/v1/vms/{name}/restart", h.RestartVM)
		r.Get("/v1/vms/{name}/exec", h.ExecVM)
		r.Post("/v1/vms/{name}/files", h.UploadFile)
		r.Get("/v1/vms/{name}/files", h.DownloadFile)

		// Invitation codes (admin only)
		r.Post("/v1/invitations", h.CreateInvitationCode)
		r.Get("/v1/invitations", h.ListInvitationCodes)
	})

	addr := ":8080"
	log.Printf("InfraBox API listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
