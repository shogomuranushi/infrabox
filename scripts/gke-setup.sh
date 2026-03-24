#!/usr/bin/env bash
# =============================================================
# InfraBox GKE Setup (Standard)
#
# Architecture:
#   - System node pool: on-demand, e2-standard-2 (nginx, cert-manager, infrabox-api)
#   - VM worker pool:   spot, e2-standard-4, autoscaling (user VM pods)
#   - Storage:         GCE PD-SSD via built-in GKE CSI driver
#   - Images:          ghcr.io/shogomuranushi/infrabox-*
#
# Usage:
#   export GCP_PROJECT=your-project
#   export DOMAIN=infrabox.example.com
#   export LETSENCRYPT_EMAIL=you@example.com
#   ./scripts/gke-setup.sh
#
# Optional:
#   GCP_ZONE=asia-northeast1-b         (default)
#   CLUSTER_NAME=infrabox              (default)
#   SYSTEM_MACHINE_TYPE=e2-standard-2  (default)
#   WORKER_MACHINE_TYPE=e2-standard-4  (default)
#   WORKER_MIN=1 / WORKER_MAX=10       (default)
#   STATIC_IP_NAME=infrabox-ip         (default)
#   ADMIN_API_KEY=<secret>             (default: auto-generated)
#   OAUTH_CLIENT_ID=<id>               (optional)
#   OAUTH_CLIENT_SECRET=<secret>       (required if OAUTH_CLIENT_ID set)
#   OAUTH_EMAIL_DOMAIN=<domain>        (required if OAUTH_CLIENT_ID set)
# =============================================================
set -euo pipefail

# --- Required ---
: "${GCP_PROJECT:?GCP_PROJECT is required}"
: "${DOMAIN:?DOMAIN is required}"
: "${LETSENCRYPT_EMAIL:?LETSENCRYPT_EMAIL is required}"

# --- Optional with defaults ---
GCP_ZONE="${GCP_ZONE:-asia-northeast1-b}"
GCP_REGION="${GCP_ZONE%-*}"
CLUSTER_NAME="${CLUSTER_NAME:-infrabox}"
SYSTEM_MACHINE_TYPE="${SYSTEM_MACHINE_TYPE:-e2-standard-2}"
WORKER_MACHINE_TYPE="${WORKER_MACHINE_TYPE:-e2-standard-4}"
WORKER_MIN="${WORKER_MIN:-1}"
WORKER_MAX="${WORKER_MAX:-10}"
STATIC_IP_NAME="${STATIC_IP_NAME:-infrabox-ip}"
ADMIN_API_KEY="${ADMIN_API_KEY:-$(openssl rand -hex 16)}"
OAUTH_CLIENT_ID="${OAUTH_CLIENT_ID:-}"
OAUTH_CLIENT_SECRET="${OAUTH_CLIENT_SECRET:-}"
OAUTH_EMAIL_DOMAIN="${OAUTH_EMAIL_DOMAIN:-}"
AUTH_DOMAIN="auth.${DOMAIN}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

log() { echo "=== $* ==="; }
ok()  { echo "✓ $*"; }

# =============================================================
log "1. Enable required GCP APIs"
# =============================================================
gcloud services enable container.googleapis.com \
  compute.googleapis.com \
  --project="$GCP_PROJECT" --quiet
ok "APIs enabled"

# =============================================================
log "2. Static IP (regional)"
# =============================================================
if gcloud compute addresses describe "$STATIC_IP_NAME" \
    --project="$GCP_PROJECT" --region="$GCP_REGION" &>/dev/null; then
  ok "Static IP '$STATIC_IP_NAME' already exists"
else
  gcloud compute addresses create "$STATIC_IP_NAME" \
    --project="$GCP_PROJECT" --region="$GCP_REGION"
  ok "Static IP reserved"
fi
STATIC_IP=$(gcloud compute addresses describe "$STATIC_IP_NAME" \
  --project="$GCP_PROJECT" --region="$GCP_REGION" --format='get(address)')
echo "  IP: $STATIC_IP"

# =============================================================
log "3. GKE Standard cluster"
# =============================================================
if gcloud container clusters describe "$CLUSTER_NAME" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  ok "Cluster '$CLUSTER_NAME' already exists"
else
  gcloud container clusters create "$CLUSTER_NAME" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --release-channel=stable \
    --no-create-default-nodepool \
    --enable-ip-alias \
    --workload-pool="${GCP_PROJECT}.svc.id.goog" \
    --quiet
  ok "Cluster created"
fi

# =============================================================
log "4. Node pools"
# =============================================================
# System pool: on-demand, tainted so only infra pods run here
if ! gcloud container node-pools describe infrabox-system \
    --cluster="$CLUSTER_NAME" --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  gcloud container node-pools create infrabox-system \
    --cluster="$CLUSTER_NAME" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --machine-type="$SYSTEM_MACHINE_TYPE" \
    --num-nodes=1 \
    --disk-type=pd-ssd \
    --disk-size=50GB \
    --node-labels=infrabox-role=api \
    --node-taints=infrabox-role=api:NoSchedule \
    --quiet
  ok "System node pool created"
else
  ok "System node pool already exists"
fi

# Worker pool: spot, autoscaling, labeled for VM workloads
if ! gcloud container node-pools describe infrabox-vms \
    --cluster="$CLUSTER_NAME" --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  gcloud container node-pools create infrabox-vms \
    --cluster="$CLUSTER_NAME" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --machine-type="$WORKER_MACHINE_TYPE" \
    --num-nodes="$WORKER_MIN" \
    --enable-autoscaling \
    --min-nodes="$WORKER_MIN" \
    --max-nodes="$WORKER_MAX" \
    --disk-type=pd-ssd \
    --disk-size=50GB \
    --node-labels=infrabox-role=vm-worker \
    --spot \
    --quiet
  ok "Worker node pool created (spot, autoscaling ${WORKER_MIN}-${WORKER_MAX})"
else
  ok "Worker node pool already exists"
fi

# =============================================================
log "5. Get credentials"
# =============================================================
gcloud container clusters get-credentials "$CLUSTER_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE"
ok "kubectl context set to cluster '$CLUSTER_NAME'"

# =============================================================
log "6. Namespaces"
# =============================================================
kubectl create ns infrabox     2>/dev/null || true
kubectl create ns infrabox-vms 2>/dev/null || true
ok "Namespaces ready"

# =============================================================
log "7. nginx-ingress (with static IP)"
# =============================================================
TOLERATION_PATCH='[{"op":"add","path":"/spec/template/spec/tolerations","value":[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]}]'
NODE_SELECTOR_PATCH='[{"op":"add","path":"/spec/template/spec/nodeSelector","value":{"infrabox-role":"api"}}]'

if ! kubectl get ns ingress-nginx &>/dev/null; then
  kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.12.0/deploy/static/provider/cloud/deploy.yaml
  kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=120s
fi

# Pin to system node pool
kubectl patch deployment ingress-nginx-controller -n ingress-nginx \
  --type=json -p="$TOLERATION_PATCH" 2>/dev/null || true
kubectl patch deployment ingress-nginx-controller -n ingress-nginx \
  --type=json -p="$NODE_SELECTOR_PATCH" 2>/dev/null || true

# Assign static IP to LoadBalancer
kubectl patch svc ingress-nginx-controller -n ingress-nginx \
  -p "{\"spec\":{\"loadBalancerIP\":\"${STATIC_IP}\"}}" 2>/dev/null || true

ok "nginx-ingress deployed (IP: $STATIC_IP)"

# =============================================================
log "8. cert-manager"
# =============================================================
if ! kubectl get ns cert-manager &>/dev/null; then
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml
  kubectl -n cert-manager rollout status deploy/cert-manager         --timeout=120s
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=120s
fi

# Pin to system node pool
for deploy in cert-manager cert-manager-webhook cert-manager-cainjector; do
  kubectl patch deployment "$deploy" -n cert-manager \
    --type=json -p="$TOLERATION_PATCH" 2>/dev/null || true
  kubectl patch deployment "$deploy" -n cert-manager \
    --type=json -p="$NODE_SELECTOR_PATCH" 2>/dev/null || true
done

# ClusterIssuer
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ${LETSENCRYPT_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        ingress:
          class: nginx
EOF

ok "cert-manager deployed"

# =============================================================
log "9. StorageClass (pd-ssd)"
# =============================================================
# GKE has the CSI driver built-in; just define the StorageClass
cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: pd-ssd
provisioner: pd.csi.storage.gke.io
parameters:
  type: pd-ssd
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
EOF
ok "StorageClass pd-ssd ready"

# =============================================================
log "10. RBAC + Secrets"
# =============================================================
kubectl apply -f "$REPO_ROOT/k8s/rbac.yaml"

kubectl create secret generic infrabox-api-secret \
  -n infrabox \
  --from-literal=api-key="${ADMIN_API_KEY}" \
  --from-literal=ingress-ip="${STATIC_IP}" \
  --dry-run=client -o yaml | kubectl apply -f -

ok "RBAC and secrets ready"

# =============================================================
log "11. Deploy infrabox-api"
# =============================================================
kubectl apply -f "$REPO_ROOT/k8s/api-deployment.yaml"

# Pin to system node pool (tolerations already in yaml, just ensure nodeSelector)
kubectl patch deployment infrabox-api -n infrabox \
  --type=json -p="$NODE_SELECTOR_PATCH" 2>/dev/null || true

# Set environment
AUTH_ENV_ARG=""
[ -n "$OAUTH_CLIENT_ID" ] && AUTH_ENV_ARG="INFRABOX_AUTH_URL=https://${AUTH_DOMAIN}"

kubectl set env deployment/infrabox-api \
  INFRABOX_INGRESS_DOMAIN="${DOMAIN}" \
  INFRABOX_STORAGE_CLASS=pd-ssd \
  INFRABOX_VM_NODE_SELECTOR=infrabox-role=vm-worker \
  INFRABOX_BASE_IMAGE=ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04 \
  ${AUTH_ENV_ARG:+"$AUTH_ENV_ARG"} \
  -n infrabox

kubectl rollout status deployment/infrabox-api -n infrabox --timeout=120s

# API ingress
sed "s/API_DOMAIN_PLACEHOLDER/api.${DOMAIN}/g" \
  "$REPO_ROOT/k8s/api-ingress.yaml" | kubectl apply -f -

ok "infrabox-api deployed"

# =============================================================
log "12. oauth2-proxy (optional)"
# =============================================================
if [ -n "$OAUTH_CLIENT_ID" ]; then
  : "${OAUTH_EMAIL_DOMAIN:?OAUTH_EMAIL_DOMAIN is required}"

  COOKIE_SECRET=$(openssl rand -base64 32 | tr -d '\n')

  kubectl create secret generic oauth2-proxy-secret \
    -n infrabox \
    --from-literal=client-id="${OAUTH_CLIENT_ID}" \
    --from-literal=client-secret="${OAUTH_CLIENT_SECRET}" \
    --from-literal=cookie-secret="${COOKIE_SECRET}" \
    --from-literal=email-domain="${OAUTH_EMAIL_DOMAIN}" \
    --from-literal=cookie-domain=".${DOMAIN}" \
    --dry-run=client -o yaml | kubectl apply -f -

  sed "s/AUTH_DOMAIN_PLACEHOLDER/${AUTH_DOMAIN}/g" \
    "$REPO_ROOT/k8s/oauth2-proxy.yaml" | kubectl apply -f -

  kubectl patch deployment oauth2-proxy -n infrabox \
    --type=json -p="$TOLERATION_PATCH" 2>/dev/null || true
  kubectl patch deployment oauth2-proxy -n infrabox \
    --type=json -p="$NODE_SELECTOR_PATCH" 2>/dev/null || true

  sed -e "s/API_DOMAIN_PLACEHOLDER/api.${DOMAIN}/g" \
      -e "s/AUTH_DOMAIN_PLACEHOLDER/${AUTH_DOMAIN}/g" \
      "$REPO_ROOT/k8s/api-ingress-auth.yaml" | kubectl apply -f -

  kubectl rollout status deployment/oauth2-proxy -n infrabox --timeout=90s
  ok "oauth2-proxy deployed (auth: $AUTH_DOMAIN)"
else
  echo "  Skipped (OAUTH_CLIENT_ID not set)"
fi

# =============================================================
log "13. Verify"
# =============================================================
echo "  Waiting for API to respond..."
for i in $(seq 1 24); do
  if curl -sf "https://api.${DOMAIN}/healthz" &>/dev/null; then
    ok "API is healthy"
    break
  fi
  sleep 5
done

echo "  Node status:"
kubectl get nodes -o wide

# =============================================================
echo ""
echo "============================================"
echo " InfraBox GKE setup complete!"
echo "============================================"
echo ""
echo " Cluster:      $CLUSTER_NAME ($GCP_ZONE)"
echo " System pool:  infrabox-system ($SYSTEM_MACHINE_TYPE, on-demand)"
echo " Worker pool:  infrabox-vms ($WORKER_MACHINE_TYPE, spot, ${WORKER_MIN}-${WORKER_MAX} nodes)"
echo ""
echo " IP:           $STATIC_IP"
echo " Domain:       $DOMAIN  (*.$DOMAIN)"
echo " Admin Key:    $ADMIN_API_KEY"
echo ""
echo " DNS: Add these records to your DNS provider:"
echo "   A  $DOMAIN     -> $STATIC_IP"
echo "   A  *.$DOMAIN   -> $STATIC_IP"
echo ""
echo " CLI config (~/.ib/config.yaml):"
echo "   endpoint: https://api.${DOMAIN}"
echo ""
echo " To deploy updates:"
echo "   ./scripts/gke-deploy.sh"
echo "============================================"
