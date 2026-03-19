#!/usr/bin/env bash
# =============================================================
# InfraBox GCE Deploy — rebuild & redeploy from latest source
#
# Usage:
#   ./scripts/gce-deploy.sh                # deploy both api + base
#   ./scripts/gce-deploy.sh api            # api image only
#   ./scripts/gce-deploy.sh base           # base image only
#
# Optional:
#   GCP_PROJECT=your-project   (default: gcloud config)
#   GCP_ZONE=asia-northeast1-a (default)
#   INSTANCE_NAME=infrabox     (default)
#   BRANCH=main                (default: current branch)
# =============================================================
set -euo pipefail

TARGET="${1:-all}"
GCP_PROJECT="${GCP_PROJECT:-$(gcloud config get-value project 2>/dev/null)}"
GCP_ZONE="${GCP_ZONE:-asia-northeast1-a}"
INSTANCE_NAME="${INSTANCE_NAME:-infrabox}"
API_NODE="${INSTANCE_NAME}-api"
BRANCH="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"

log() { echo "=== $* ==="; }
ok()  { echo "✓ $*"; }
err() { echo "✗ $*" >&2; exit 1; }

SSH_CMD="gcloud compute ssh $API_NODE --project=$GCP_PROJECT --zone=$GCP_ZONE --"

# ---------------------------------------------------------
# 1. Build & import images on API node
# ---------------------------------------------------------
build_api() {
  log "Building API image on $API_NODE"
  $SSH_CMD "
    set -e
    cd /tmp
    if [ -d infrabox-src ]; then
      cd infrabox-src && git fetch origin $BRANCH && git checkout $BRANCH && git reset --hard origin/$BRANCH
    else
      git clone --depth 1 -b $BRANCH https://github.com/shogomuranushi/infrabox.git infrabox-src
      cd infrabox-src
    fi

    docker build -t infrabox-api:latest -f api/Dockerfile api/
    docker save infrabox-api:latest | sudo k3s ctr images import -
    echo 'API image built and imported'
  "
  ok "API image updated"
}

restart_api() {
  log "Restarting API deployment"
  $SSH_CMD "
    set -e
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    kubectl rollout restart deployment/infrabox-api -n infrabox
    kubectl rollout status deployment/infrabox-api -n infrabox --timeout=90s
  "
  ok "API deployment restarted"
}

build_base() {
  log "Building base image on $API_NODE"
  $SSH_CMD "
    set -e
    cd /tmp
    if [ -d infrabox-src ]; then
      cd infrabox-src && git fetch origin $BRANCH && git checkout $BRANCH && git reset --hard origin/$BRANCH
    else
      git clone --depth 1 -b $BRANCH https://github.com/shogomuranushi/infrabox.git infrabox-src
      cd infrabox-src
    fi

    docker build -t infrabox-base:ubuntu-24.04 -f images/base/Dockerfile images/base/
    docker save infrabox-base:ubuntu-24.04 | sudo k3s ctr images import -
    echo 'Base image built and imported'
  "
  ok "Base image updated on API node"

  # Also update on worker nodes
  log "Updating base image on worker nodes"
  WORKERS=$(gcloud compute instances list \
    --project="$GCP_PROJECT" \
    --filter="name~'^${INSTANCE_NAME}-worker' AND status=RUNNING" \
    --format='value(name,zone)' 2>/dev/null) || true

  if [ -z "$WORKERS" ]; then
    echo "  No running worker nodes found, skipping"
    return
  fi

  while IFS=$'\t' read -r name zone; do
    log "Updating base image on $name"
    gcloud compute ssh "$name" --project="$GCP_PROJECT" --zone="$zone" -- "
      set -e
      cd /tmp
      if [ -d infrabox-src ]; then
        cd infrabox-src && git fetch origin $BRANCH && git checkout $BRANCH && git reset --hard origin/$BRANCH
      else
        git clone --depth 1 -b $BRANCH https://github.com/shogomuranushi/infrabox.git infrabox-src
        cd infrabox-src
      fi

      docker build -t infrabox-base:ubuntu-24.04 -f images/base/Dockerfile images/base/
      docker save infrabox-base:ubuntu-24.04 | k3s ctr images import -
      echo 'Base image built and imported'
    "
    ok "Base image updated on $name"
  done <<< "$WORKERS"
}

# ---------------------------------------------------------
# 2. Execute
# ---------------------------------------------------------
case "$TARGET" in
  api)
    build_api
    restart_api
    ;;
  base)
    build_base
    ;;
  all)
    build_api
    build_base
    restart_api
    ;;
  *)
    err "Unknown target: $TARGET (use: api, base, or all)"
    ;;
esac

log "Deploy complete"
