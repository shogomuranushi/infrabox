#!/usr/bin/env bash
# =============================================================
# InfraBox GKE Deploy — build, push, and redeploy API image
#
# Usage:
#   ./scripts/gke-deploy.sh                # deploy api (default)
#   ./scripts/gke-deploy.sh api            # api image only
#   ./scripts/gke-deploy.sh base           # base image only
#
# Optional:
#   GCP_PROJECT=your-project
#   GCP_ZONE=asia-northeast1-b  (default)
#   CLUSTER_NAME=infrabox        (default)
#   GHCR_USER=shogomuranushi     (default)
# =============================================================
set -euo pipefail

TARGET="${1:-api}"
GCP_PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null)}"
GCP_ZONE="${GCP_ZONE:-asia-northeast1-b}"
CLUSTER_NAME="${CLUSTER_NAME:-infrabox}"
GHCR_USER="${GHCR_USER:-shogomuranushi}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

log() { echo "=== $* ==="; }
ok()  { echo "✓ $*"; }
err() { echo "✗ $*" >&2; exit 1; }

deploy_api() {
  log "Building and pushing API image"
  docker build --platform linux/amd64 -t "ghcr.io/${GHCR_USER}/infrabox-api:latest" \
    -f "$REPO_ROOT/api/Dockerfile" "$REPO_ROOT/api/"
  docker push "ghcr.io/${GHCR_USER}/infrabox-api:latest"
  ok "API image pushed"

  log "Restarting API deployment"
  gcloud container clusters get-credentials "$CLUSTER_NAME" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" --quiet
  kubectl rollout restart deployment/infrabox-api -n infrabox
  kubectl rollout status deployment/infrabox-api -n infrabox --timeout=90s
  ok "API deployment restarted"
}

deploy_base() {
  log "Building and pushing base image"
  docker build -t "ghcr.io/${GHCR_USER}/infrabox-base:ubuntu-24.04" \
    -f "$REPO_ROOT/images/base/Dockerfile" "$REPO_ROOT/images/base/"
  docker push "ghcr.io/${GHCR_USER}/infrabox-base:ubuntu-24.04"
  ok "Base image pushed to ghcr.io"
  echo "  Note: existing VMs will pull the new image on next restart."
}

case "$TARGET" in
  api)  deploy_api ;;
  base) deploy_base ;;
  all)  deploy_api; deploy_base ;;
  *)    err "Unknown target: $TARGET (use: api, base, or all)" ;;
esac

log "Deploy complete"
