#!/usr/bin/env bash
# =============================================================
# InfraBox GCE Setup
# Usage:
#   export GCP_PROJECT=your-project
#   export DOMAIN=infrabox.example.com
#   export LETSENCRYPT_EMAIL=you@example.com
#   ./scripts/gce-setup.sh
#
# Optional:
#   GCP_ZONE=asia-northeast1-a   (default)
#   INSTANCE_NAME=infrabox-k3s   (default)
#   MACHINE_TYPE=e2-medium       (default)
#   SPOT=true                    (default: true, use false for non-preemptible)
#   STATIC_IP_NAME=infrabox-ip   (default)
#   ALLOWED_CIDRS=1.2.3.4/32,5.6.7.8/32  (optional, restricts SSH/HTTPS/API)
#   ADMIN_API_KEY=<secret>       (default: auto-generated)
#   OAUTH_CLIENT_ID=<id>         (optional, enables Google Workspace auth)
#   OAUTH_CLIENT_SECRET=<secret> (required if OAUTH_CLIENT_ID is set)
#   OAUTH_EMAIL_DOMAIN=<domain>  (default: same as DOMAIN base, e.g. example.com)
# =============================================================
set -euo pipefail

# --- Required params ---
: "${GCP_PROJECT:?GCP_PROJECT is required}"
: "${DOMAIN:?DOMAIN is required}"
: "${LETSENCRYPT_EMAIL:?LETSENCRYPT_EMAIL is required}"

# --- Optional params with defaults ---
GCP_ZONE="${GCP_ZONE:-asia-northeast1-a}"
GCP_REGION="${GCP_ZONE%-*}"
INSTANCE_NAME="${INSTANCE_NAME:-infrabox-k3s}"
MACHINE_TYPE="${MACHINE_TYPE:-e2-medium}"
SPOT="${SPOT:-true}"
STATIC_IP_NAME="${STATIC_IP_NAME:-infrabox-ip}"
ALLOWED_CIDRS="${ALLOWED_CIDRS:-}"
ADMIN_API_KEY="${ADMIN_API_KEY:-$(openssl rand -hex 16)}"
OAUTH_CLIENT_ID="${OAUTH_CLIENT_ID:-}"
OAUTH_CLIENT_SECRET="${OAUTH_CLIENT_SECRET:-}"
OAUTH_EMAIL_DOMAIN="${OAUTH_EMAIL_DOMAIN:-}"
AUTH_DOMAIN="auth.${DOMAIN}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

log() { echo "=== $* ==="; }
ok()  { echo "✓ $*"; }

# =============================================================
log "1. Static IP"
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
log "2. GCE Instance"
# =============================================================
if gcloud compute instances describe "$INSTANCE_NAME" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  STATUS=$(gcloud compute instances describe "$INSTANCE_NAME" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" --format='get(status)')
  if [ "$STATUS" = "TERMINATED" ]; then
    gcloud compute instances start "$INSTANCE_NAME" \
      --project="$GCP_PROJECT" --zone="$GCP_ZONE"
    ok "Instance started"
  else
    ok "Instance already running"
  fi
else
  SPOT_FLAG=""
  [ "$SPOT" = "true" ] && SPOT_FLAG="--provisioning-model=SPOT --instance-termination-action=STOP"

  gcloud compute instances create "$INSTANCE_NAME" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --machine-type="$MACHINE_TYPE" \
    --image-family=ubuntu-2404-lts \
    --image-project=ubuntu-os-cloud \
    --boot-disk-size=50GB \
    --address="$STATIC_IP" \
    --tags=infrabox \
    $SPOT_FLAG
  ok "Instance created"

  echo "  Waiting for SSH to be ready..."
  sleep 30
fi

# =============================================================
log "3. Firewall rules"
# =============================================================
# Always allow health check from GCP
if ! gcloud compute firewall-rules describe infrabox-allow-health \
    --project="$GCP_PROJECT" &>/dev/null; then
  gcloud compute firewall-rules create infrabox-allow-health \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:80 \
    --source-ranges=130.211.0.0/22,35.191.0.0/16 \
    --description="Let's Encrypt HTTP-01 challenge"
fi

if [ -n "$ALLOWED_CIDRS" ]; then
  # Restricted access
  for rule in infrabox-allow-https infrabox-allow-ssh infrabox-allow-api; do
    gcloud compute firewall-rules delete "$rule" \
      --project="$GCP_PROJECT" --quiet 2>/dev/null || true
  done
  gcloud compute firewall-rules create infrabox-allow-https \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:443,tcp:80 \
    --source-ranges="$ALLOWED_CIDRS" \
    --description="InfraBox HTTPS (IP restricted)"
  gcloud compute firewall-rules create infrabox-allow-ssh \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:2222 \
    --source-ranges="$ALLOWED_CIDRS" \
    --description="InfraBox SSH via sshpiper (IP restricted)"
  gcloud compute firewall-rules create infrabox-allow-api \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:30080 \
    --source-ranges="$ALLOWED_CIDRS" \
    --description="InfraBox API (IP restricted)"
  ok "Firewall rules created (restricted to: $ALLOWED_CIDRS)"
else
  # Open access
  for rule in infrabox-allow-https infrabox-allow-ssh infrabox-allow-api; do
    gcloud compute firewall-rules delete "$rule" \
      --project="$GCP_PROJECT" --quiet 2>/dev/null || true
  done
  gcloud compute firewall-rules create infrabox-allow-https \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:443,tcp:80 \
    --source-ranges=0.0.0.0/0
  gcloud compute firewall-rules create infrabox-allow-ssh \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:2222 \
    --source-ranges=0.0.0.0/0
  gcloud compute firewall-rules create infrabox-allow-api \
    --project="$GCP_PROJECT" \
    --target-tags=infrabox \
    --allow=tcp:30080 \
    --source-ranges=0.0.0.0/0
  ok "Firewall rules created (open)"
fi

# =============================================================
log "4. k3s + tools installation"
# =============================================================
gcloud compute ssh "$INSTANCE_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
# k3s
if ! command -v k3s &>/dev/null; then
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='--disable=traefik' sh -
  sudo chmod 644 /etc/rancher/k3s/k3s.yaml
  echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' >> ~/.bashrc
fi
# Docker (for building images)
if ! command -v docker &>/dev/null; then
  curl -fsSL https://get.docker.com | sh
  sudo usermod -aG docker \$USER
fi
echo 'k3s and docker ready'
"
ok "k3s installed"

# =============================================================
log "5. Build Docker images"
# =============================================================
# Transfer source
tar czf /tmp/infrabox-src.tar.gz -C "$REPO_ROOT" api/ images/
gcloud compute scp /tmp/infrabox-src.tar.gz \
  "$INSTANCE_NAME":/tmp/ \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE"

gcloud compute ssh "$INSTANCE_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
cd /tmp && tar xzf infrabox-src.tar.gz 2>/dev/null

# Base image
docker build -t infrabox-base:ubuntu-24.04 -f images/base/Dockerfile images/base/
docker save infrabox-base:ubuntu-24.04 | sudo k3s ctr images import -

# API image
docker build -t infrabox-api:latest -f api/Dockerfile api/
docker save infrabox-api:latest | sudo k3s ctr images import -

echo 'images built and imported'
"
ok "Docker images built"

# =============================================================
log "6. Kubernetes setup"
# =============================================================
gcloud compute ssh "$INSTANCE_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# Wait for k3s to be ready
for i in \$(seq 1 20); do
  kubectl get nodes &>/dev/null && break
  echo '  Waiting for k3s...'
  sleep 5
done

# Namespaces
kubectl create ns infrabox     2>/dev/null || true
kubectl create ns infrabox-vms 2>/dev/null || true
kubectl create ns sshpiper     2>/dev/null || true

# cert-manager
if ! kubectl get ns cert-manager &>/dev/null; then
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml
  kubectl -n cert-manager rollout status deploy/cert-manager --timeout=120s
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=120s
fi

# nginx-ingress
if ! kubectl get ns ingress-nginx &>/dev/null; then
  kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.12.0/deploy/static/provider/baremetal/deploy.yaml
  kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=120s
fi

# sshpiper CRD + deployment
if ! kubectl get deployment sshpiper -n infrabox &>/dev/null; then
  kubectl apply -f https://raw.githubusercontent.com/tg123/sshpiper/master/hack/kubernetes/sshpiperd.yaml -n infrabox
fi

# Patch sshpiper to use hostPort 2222
kubectl patch deployment sshpiper -n infrabox --type='json' -p='[
  {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/ports\",
   \"value\":[{\"containerPort\":2222,\"hostPort\":2222}]}
]' 2>/dev/null || true

echo 'k8s components ready'
"
ok "Kubernetes components deployed"

# =============================================================
log "7. Secrets and RBAC"
# =============================================================
gcloud compute ssh "$INSTANCE_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# sshpiper upstream keypair
if ! kubectl get secret sshpiper-upstream-key -n infrabox &>/dev/null; then
  ssh-keygen -t ed25519 -N '' -f /tmp/upstream-key
  kubectl create secret generic sshpiper-upstream-key \
    -n infrabox \
    --from-file=ssh-privatekey=/tmp/upstream-key
  rm -f /tmp/upstream-key /tmp/upstream-key.pub
fi
# Copy to infrabox-vms namespace
kubectl get secret sshpiper-upstream-key -n infrabox -o json \
  | python3 -c \"
import json,sys
s=json.load(sys.stdin)
del s['metadata']['resourceVersion']
del s['metadata']['uid']
del s['metadata']['creationTimestamp']
s['metadata']['namespace']='infrabox-vms'
print(json.dumps(s))
\" | kubectl apply -f - 2>/dev/null || true

# sshpiper server key
if ! kubectl get secret sshpiper-server-key -n infrabox &>/dev/null; then
  ssh-keygen -t ed25519 -N '' -f /tmp/server-key
  kubectl create secret generic sshpiper-server-key \
    -n infrabox \
    --from-file=ssh-hostkey=/tmp/server-key
  rm -f /tmp/server-key /tmp/server-key.pub
fi

# API secret
kubectl create secret generic infrabox-api-secret \
  -n infrabox \
  --from-literal=api-key='${ADMIN_API_KEY}' \
  --from-literal=ingress-ip='${STATIC_IP}' \
  --from-literal=sshpiper-ip='${STATIC_IP}' \
  --dry-run=client -o yaml | kubectl apply -f -

echo 'secrets ready'
"
ok "Secrets created"

# =============================================================
log "8. Deploy infrabox-api"
# =============================================================
# Transfer k8s manifests
tar czf /tmp/infrabox-k8s.tar.gz -C "$REPO_ROOT" k8s/
gcloud compute scp /tmp/infrabox-k8s.tar.gz \
  "$INSTANCE_NAME":/tmp/ \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE"

gcloud compute ssh "$INSTANCE_NAME" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
cd /tmp && tar xzf infrabox-k8s.tar.gz 2>/dev/null

kubectl apply -f k8s/rbac.yaml
kubectl apply -f k8s/api-deployment.yaml

# Set domain and auth URL
AUTH_ENV=""
[ -n "${OAUTH_CLIENT_ID}" ] && AUTH_ENV="INFRABOX_AUTH_URL=https://${AUTH_DOMAIN}"
kubectl set env deployment/infrabox-api \
  INFRABOX_INGRESS_DOMAIN='${DOMAIN}' \
  ${AUTH_ENV} \
  -n infrabox

kubectl rollout status deployment/infrabox-api -n infrabox --timeout=90s

# Let's Encrypt ClusterIssuer
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

# HTTPS ingress for the API (api.<DOMAIN>)
sed 's/API_DOMAIN_PLACEHOLDER/api.${DOMAIN}/g' /tmp/k8s/api-ingress.yaml \
  | kubectl apply -f -

echo 'infrabox-api deployed'
"
ok "infrabox-api deployed"

# =============================================================
log "9. oauth2-proxy (Google Workspace auth)"
# =============================================================
if [ -n "$OAUTH_CLIENT_ID" ]; then
  : "${OAUTH_EMAIL_DOMAIN:?OAUTH_EMAIL_DOMAIN is required (e.g. example.com)}"
  gcloud compute ssh "$INSTANCE_NAME" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# oauth2-proxy secret
kubectl create secret generic oauth2-proxy-secret \
  -n infrabox \
  --from-literal=client-id='${OAUTH_CLIENT_ID}' \
  --from-literal=client-secret='${OAUTH_CLIENT_SECRET}' \
  --from-literal=cookie-secret='\$(openssl rand -base64 32 | tr -d '\n')' \
  --from-literal=email-domain='${OAUTH_EMAIL_DOMAIN}' \
  --from-literal=cookie-domain='.${DOMAIN}' \
  --dry-run=client -o yaml | kubectl apply -f -

# Apply oauth2-proxy with auth domain substituted
sed 's/AUTH_DOMAIN_PLACEHOLDER/${AUTH_DOMAIN}/g' /tmp/k8s/oauth2-proxy.yaml \
  | kubectl apply -f -

# Auth ingress for /v1/keys (Google auth required)
sed -e 's/API_DOMAIN_PLACEHOLDER/api.${DOMAIN}/g' \
    -e 's/AUTH_DOMAIN_PLACEHOLDER/${AUTH_DOMAIN}/g' \
    /tmp/k8s/api-ingress-auth.yaml \
  | kubectl apply -f -

kubectl rollout status deployment/oauth2-proxy -n infrabox --timeout=90s
echo 'oauth2-proxy deployed'
"
  ok "oauth2-proxy deployed (auth: $AUTH_DOMAIN)"
else
  echo "  Skipped (OAUTH_CLIENT_ID not set)"
fi

# =============================================================
log "10. Verify"
# =============================================================
echo "  Waiting for API to respond..."
for i in $(seq 1 12); do
  if curl -sf "http://${STATIC_IP}:30080/healthz" &>/dev/null; then
    ok "API is healthy"
    break
  fi
  sleep 5
done

# =============================================================
echo ""
echo "============================================"
echo " InfraBox setup complete!"
echo "============================================"
echo ""
echo " IP:         $STATIC_IP"
echo " Domain:     $DOMAIN  (*.${DOMAIN})"
echo " Admin Key:  $ADMIN_API_KEY"
echo ""
echo " DNS: Add these records to your DNS provider:"
echo "   A  infrabox.<base-domain>   -> $STATIC_IP"
echo "   A  *.infrabox.<base-domain> -> $STATIC_IP"
echo ""
echo " CLI config (~/.ib/config.yaml):"
echo "   endpoint:    https://api.${DOMAIN}"
echo "   sshpiper_ip: ${STATIC_IP}"
echo ""
echo " Build CLI:"
echo "   cd cli && go build \\"
echo "     -ldflags \"-X .../cmd.defaultEndpoint=http://${STATIC_IP}:30080 \\"
echo "              -X .../cmd.defaultSSHPiperIP=${STATIC_IP}\" \\"
echo "     -o ib ."
echo "============================================"
