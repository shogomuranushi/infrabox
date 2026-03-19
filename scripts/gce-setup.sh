#!/usr/bin/env bash
# =============================================================
# InfraBox GCE Setup (Two-node architecture)
#
# Architecture:
#   - API node:    on-demand, k3s server + control plane workloads
#   - Worker node: spot, MIG-managed, k3s agent + VM workloads
#   - Storage:     GCE PD via CSI driver
#
# Usage:
#   export GCP_PROJECT=your-project
#   export DOMAIN=infrabox.abejatech.com
#   export LETSENCRYPT_EMAIL=you@example.com
#   ./scripts/gce-setup.sh
#
# Optional:
#   GCP_ZONE=asia-northeast1-a        (default)
#   INSTANCE_NAME=infrabox             (default, base name for resources)
#   API_MACHINE_TYPE=e2-small          (default, on-demand)
#   WORKER_MACHINE_TYPE=n2d-standard-4 (default, spot)
#   WORKER_COUNT=1                     (default)
#   STATIC_IP_NAME=infrabox-ip         (default)
#   ALLOWED_CIDRS=1.2.3.4/32,5.6.7.8/32  (optional)
#   ADMIN_API_KEY=<secret>             (default: auto-generated)
#   OAUTH_CLIENT_ID=<id>               (optional)
#   OAUTH_CLIENT_SECRET=<secret>       (required if OAUTH_CLIENT_ID)
#   OAUTH_EMAIL_DOMAIN=<domain>        (required if OAUTH_CLIENT_ID)
# =============================================================
set -euo pipefail

# --- Required params ---
: "${GCP_PROJECT:?GCP_PROJECT is required}"
: "${DOMAIN:?DOMAIN is required}"
: "${LETSENCRYPT_EMAIL:?LETSENCRYPT_EMAIL is required}"

# --- Optional params with defaults ---
GCP_ZONE="${GCP_ZONE:-asia-northeast1-a}"
GCP_REGION="${GCP_ZONE%-*}"
INSTANCE_NAME="${INSTANCE_NAME:-infrabox}"
API_MACHINE_TYPE="${API_MACHINE_TYPE:-e2-small}"
WORKER_MACHINE_TYPE="${WORKER_MACHINE_TYPE:-n2d-standard-4}"
WORKER_COUNT="${WORKER_COUNT:-1}"
STATIC_IP_NAME="${STATIC_IP_NAME:-infrabox-ip}"
ALLOWED_CIDRS="${ALLOWED_CIDRS:-}"
ADMIN_API_KEY="${ADMIN_API_KEY:-$(openssl rand -hex 16)}"
K3S_TOKEN="${K3S_TOKEN:-$(openssl rand -hex 32)}"
OAUTH_CLIENT_ID="${OAUTH_CLIENT_ID:-}"
OAUTH_CLIENT_SECRET="${OAUTH_CLIENT_SECRET:-}"
OAUTH_EMAIL_DOMAIN="${OAUTH_EMAIL_DOMAIN:-}"
AUTH_DOMAIN="auth.${DOMAIN}"

API_NODE="${INSTANCE_NAME}-api"
WORKER_TEMPLATE="${INSTANCE_NAME}-worker-tmpl"
WORKER_MIG="${INSTANCE_NAME}-worker-mig"
WORKER_HC="${INSTANCE_NAME}-worker-hc"

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
log "2. Firewall rules"
# =============================================================
# Health check for Let's Encrypt
if ! gcloud compute firewall-rules describe "${INSTANCE_NAME}-allow-health" \
    --project="$GCP_PROJECT" &>/dev/null; then
  gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-health" \
    --project="$GCP_PROJECT" \
    --target-tags="${INSTANCE_NAME}-api" \
    --allow=tcp:80 \
    --source-ranges=130.211.0.0/22,35.191.0.0/16 \
    --description="Let's Encrypt HTTP-01 challenge"
fi

SOURCE_RANGES="${ALLOWED_CIDRS:-0.0.0.0/0}"

for rule in "${INSTANCE_NAME}-allow-https" "${INSTANCE_NAME}-allow-ssh" "${INSTANCE_NAME}-allow-api"; do
  gcloud compute firewall-rules delete "$rule" \
    --project="$GCP_PROJECT" --quiet 2>/dev/null || true
done

gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-https" \
  --project="$GCP_PROJECT" \
  --target-tags="${INSTANCE_NAME}-api" \
  --allow=tcp:443,tcp:80 \
  --source-ranges="$SOURCE_RANGES"
gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-ssh" \
  --project="$GCP_PROJECT" \
  --target-tags="${INSTANCE_NAME}-api" \
  --allow=tcp:2222 \
  --source-ranges="$SOURCE_RANGES"
gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-api" \
  --project="$GCP_PROJECT" \
  --target-tags="${INSTANCE_NAME}-api" \
  --allow=tcp:30080 \
  --source-ranges="$SOURCE_RANGES"

# Internal k3s communication between API and worker nodes
if ! gcloud compute firewall-rules describe "${INSTANCE_NAME}-allow-internal" \
    --project="$GCP_PROJECT" &>/dev/null; then
  gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-internal" \
    --project="$GCP_PROJECT" \
    --source-tags="${INSTANCE_NAME}-api,${INSTANCE_NAME}-worker" \
    --target-tags="${INSTANCE_NAME}-api,${INSTANCE_NAME}-worker" \
    --allow=tcp:6443,tcp:10250,tcp:8472,tcp:51820,tcp:51821,udp:8472,udp:51820,udp:51821 \
    --description="k3s internal communication"
fi

# MIG health check
if ! gcloud compute firewall-rules describe "${INSTANCE_NAME}-allow-mig-health" \
    --project="$GCP_PROJECT" &>/dev/null; then
  gcloud compute firewall-rules create "${INSTANCE_NAME}-allow-mig-health" \
    --project="$GCP_PROJECT" \
    --target-tags="${INSTANCE_NAME}-worker" \
    --allow=tcp:10256 \
    --source-ranges=130.211.0.0/22,35.191.0.0/16 \
    --description="MIG autohealing health check"
fi

ok "Firewall rules created"

# =============================================================
log "3. API Node (on-demand, k3s server)"
# =============================================================
if gcloud compute instances describe "$API_NODE" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  STATUS=$(gcloud compute instances describe "$API_NODE" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" --format='get(status)')
  if [ "$STATUS" = "TERMINATED" ]; then
    gcloud compute instances start "$API_NODE" \
      --project="$GCP_PROJECT" --zone="$GCP_ZONE"
    ok "API node started"
  else
    ok "API node already running"
  fi
else
  gcloud compute instances create "$API_NODE" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --machine-type="$API_MACHINE_TYPE" \
    --image-family=ubuntu-2404-lts \
    --image-project=ubuntu-os-cloud \
    --boot-disk-size=20GB \
    --boot-disk-type=pd-ssd \
    --address="$STATIC_IP" \
    --tags="${INSTANCE_NAME}-api"
  ok "API node created"
  echo "  Waiting for SSH to be ready..."
  sleep 30
fi

# =============================================================
log "4. k3s server setup on API node"
# =============================================================
gcloud compute ssh "$API_NODE" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
# k3s server with node taint (only infra pods run here)
if ! command -v k3s &>/dev/null; then
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='--disable=traefik --node-taint infrabox-role=api:NoSchedule --node-label infrabox-role=api' K3S_TOKEN='${K3S_TOKEN}' sh -
  sudo chmod 644 /etc/rancher/k3s/k3s.yaml
  echo 'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml' >> ~/.bashrc
fi
# Docker (for building images)
if ! command -v docker &>/dev/null; then
  curl -fsSL https://get.docker.com | sh
  sudo usermod -aG docker \$USER
fi
echo 'k3s server and docker ready'
"
ok "k3s server installed on API node"

# =============================================================
log "5. Build and import Docker images on API node"
# =============================================================
tar czf /tmp/infrabox-src.tar.gz -C "$REPO_ROOT" api/ images/
gcloud compute scp /tmp/infrabox-src.tar.gz \
  "$API_NODE":/tmp/ \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE"

gcloud compute ssh "$API_NODE" \
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
ok "Docker images built on API node"

# =============================================================
log "6. Kubernetes components on API node"
# =============================================================
gcloud compute ssh "$API_NODE" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

for i in \$(seq 1 20); do
  kubectl get nodes &>/dev/null && break
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

# sshpiper
if ! kubectl get deployment sshpiper -n infrabox &>/dev/null; then
  kubectl apply -f https://raw.githubusercontent.com/tg123/sshpiper/master/hack/kubernetes/sshpiperd.yaml -n infrabox
fi
kubectl patch deployment sshpiper -n infrabox --type='json' -p='[
  {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/ports\",
   \"value\":[{\"containerPort\":2222,\"hostPort\":2222}]}
]' 2>/dev/null || true

# GCE PD CSI Driver
kubectl apply -k 'https://github.com/kubernetes-sigs/gcp-compute-persistent-disk-csi-driver/deploy/kubernetes/overlays/stable/?ref=v1.15.1' 2>/dev/null || true

# Wait for CSI driver
for i in \$(seq 1 30); do
  kubectl get csidrivers pd.csi.storage.gke.io &>/dev/null && break
  sleep 5
done

# StorageClass for PD-SSD
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

# Tolerations for infra pods to run on tainted API node
for ns_deploy in 'ingress-nginx/ingress-nginx-controller' 'cert-manager/cert-manager' 'cert-manager/cert-manager-webhook' 'cert-manager/cert-manager-cainjector'; do
  NS=\${ns_deploy%%/*}
  DEPLOY=\${ns_deploy##*/}
  kubectl patch deployment \"\$DEPLOY\" -n \"\$NS\" --type='json' -p='[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/tolerations\",
     \"value\":[{\"key\":\"infrabox-role\",\"operator\":\"Equal\",\"value\":\"api\",\"effect\":\"NoSchedule\"}]}
  ]' 2>/dev/null || true
done
kubectl patch deployment sshpiper -n infrabox --type='json' -p='[
  {\"op\":\"add\",\"path\":\"/spec/template/spec/tolerations\",
   \"value\":[{\"key\":\"infrabox-role\",\"operator\":\"Equal\",\"value\":\"api\",\"effect\":\"NoSchedule\"}]}
]' 2>/dev/null || true

echo 'k8s components ready'
"
ok "Kubernetes components deployed on API node"

# =============================================================
log "7. Secrets and RBAC"
# =============================================================
gcloud compute ssh "$API_NODE" \
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
tar czf /tmp/infrabox-k8s.tar.gz -C "$REPO_ROOT" k8s/
gcloud compute scp /tmp/infrabox-k8s.tar.gz \
  "$API_NODE":/tmp/ \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE"

gcloud compute ssh "$API_NODE" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
cd /tmp && tar xzf infrabox-k8s.tar.gz 2>/dev/null

kubectl apply -f k8s/rbac.yaml
kubectl apply -f k8s/api-deployment.yaml

# Toleration + nodeSelector so API runs on tainted API node
kubectl patch deployment infrabox-api -n infrabox --type='json' -p='[
  {\"op\":\"add\",\"path\":\"/spec/template/spec/tolerations\",
   \"value\":[{\"key\":\"infrabox-role\",\"operator\":\"Equal\",\"value\":\"api\",\"effect\":\"NoSchedule\"}]},
  {\"op\":\"add\",\"path\":\"/spec/template/spec/nodeSelector\",
   \"value\":{\"infrabox-role\":\"api\"}}
]'

# Set environment for PD storage and VM node scheduling
AUTH_ENV=\"\"
[ -n '${OAUTH_CLIENT_ID}' ] && AUTH_ENV='INFRABOX_AUTH_URL=https://${AUTH_DOMAIN}'
kubectl set env deployment/infrabox-api \
  INFRABOX_INGRESS_DOMAIN='${DOMAIN}' \
  INFRABOX_STORAGE_CLASS='pd-ssd' \
  INFRABOX_VM_NODE_SELECTOR='infrabox-role=vm-worker' \
  INFRABOX_BASE_IMAGE='infrabox-base:ubuntu-24.04' \
  \${AUTH_ENV} \
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

# API ingress
sed 's/API_DOMAIN_PLACEHOLDER/api.${DOMAIN}/g' /tmp/k8s/api-ingress.yaml \
  | kubectl apply -f -

echo 'infrabox-api deployed'
"
ok "infrabox-api deployed"

# =============================================================
log "9. oauth2-proxy (Google Workspace auth)"
# =============================================================
if [ -n "$OAUTH_CLIENT_ID" ]; then
  : "${OAUTH_EMAIL_DOMAIN:?OAUTH_EMAIL_DOMAIN is required (e.g. abejainc.com)}"
  gcloud compute ssh "$API_NODE" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
set -e
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

kubectl create secret generic oauth2-proxy-secret \
  -n infrabox \
  --from-literal=client-id='${OAUTH_CLIENT_ID}' \
  --from-literal=client-secret='${OAUTH_CLIENT_SECRET}' \
  --from-literal=cookie-secret='\$(openssl rand -base64 32 | tr -d '\n')' \
  --from-literal=email-domain='${OAUTH_EMAIL_DOMAIN}' \
  --from-literal=cookie-domain='.${DOMAIN}' \
  --dry-run=client -o yaml | kubectl apply -f -

sed 's/AUTH_DOMAIN_PLACEHOLDER/${AUTH_DOMAIN}/g' /tmp/k8s/oauth2-proxy.yaml \
  | kubectl apply -f -

# Toleration for API node
kubectl patch deployment oauth2-proxy -n infrabox --type='json' -p='[
  {\"op\":\"add\",\"path\":\"/spec/template/spec/tolerations\",
   \"value\":[{\"key\":\"infrabox-role\",\"operator\":\"Equal\",\"value\":\"api\",\"effect\":\"NoSchedule\"}]}
]' 2>/dev/null || true

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
log "10. Worker MIG (spot, k3s agent)"
# =============================================================
# Health check for MIG autohealing
if ! gcloud compute health-checks describe "$WORKER_HC" \
    --project="$GCP_PROJECT" &>/dev/null; then
  gcloud compute health-checks create tcp "$WORKER_HC" \
    --project="$GCP_PROJECT" \
    --port=10250 \
    --check-interval=30 \
    --timeout=10 \
    --healthy-threshold=2 \
    --unhealthy-threshold=3
  ok "Worker health check created"
fi

# Instance template for worker
if gcloud compute instance-templates describe "$WORKER_TEMPLATE" \
    --project="$GCP_PROJECT" &>/dev/null; then
  ok "Worker instance template already exists"
else
  gcloud compute instance-templates create "$WORKER_TEMPLATE" \
    --project="$GCP_PROJECT" \
    --machine-type="$WORKER_MACHINE_TYPE" \
    --image-family=ubuntu-2404-lts \
    --image-project=ubuntu-os-cloud \
    --boot-disk-size=20GB \
    --boot-disk-type=pd-ssd \
    --tags="${INSTANCE_NAME}-worker" \
    --scopes=cloud-platform \
    --provisioning-model=SPOT \
    --instance-termination-action=STOP \
    --metadata=startup-script="#!/bin/bash
set -euo pipefail
exec > >(tee /var/log/infrabox-worker-startup.log) 2>&1
log() { echo \"=== \$(date '+%H:%M:%S') \$* ===\"; }

K3S_TOKEN='${K3S_TOKEN}'
API_IP='${STATIC_IP}'

log '1. Install k3s agent'
curl -sfL https://get.k3s.io | K3S_URL=\"https://\${API_IP}:6443\" K3S_TOKEN=\"\${K3S_TOKEN}\" INSTALL_K3S_EXEC='--node-label=infrabox-role=vm-worker' sh -
for i in \$(seq 1 30); do
  k3s kubectl get nodes &>/dev/null && break
  sleep 5
done

log '2. Install Docker'
curl -fsSL https://get.docker.com | sh

log '3. Build and import base image'
cd /tmp
apt-get update -qq && apt-get install -y -qq git
git clone --depth 1 https://github.com/shogomuranushi/infrabox.git infrabox-src
cd infrabox-src

docker build -t infrabox-base:ubuntu-24.04 -f images/base/Dockerfile images/base/
docker save infrabox-base:ubuntu-24.04 | k3s ctr images import -

log 'Worker setup complete!'
"
  ok "Worker instance template created"
fi

# MIG
if gcloud compute instance-groups managed describe "$WORKER_MIG" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" &>/dev/null; then
  ok "Worker MIG already exists"
  gcloud compute instance-groups managed resize "$WORKER_MIG" \
    --project="$GCP_PROJECT" --zone="$GCP_ZONE" \
    --size="$WORKER_COUNT" 2>/dev/null || true
else
  gcloud compute instance-groups managed create "$WORKER_MIG" \
    --project="$GCP_PROJECT" \
    --zone="$GCP_ZONE" \
    --template="$WORKER_TEMPLATE" \
    --size="$WORKER_COUNT" \
    --health-check="$WORKER_HC" \
    --initial-delay=300
  ok "Worker MIG created (size=$WORKER_COUNT)"
fi

# =============================================================
log "11. Verify"
# =============================================================
echo "  Waiting for API to respond..."
for i in $(seq 1 12); do
  if curl -sf "http://${STATIC_IP}:30080/healthz" &>/dev/null; then
    ok "API is healthy"
    break
  fi
  sleep 5
done

echo "  Waiting for worker node to join..."
gcloud compute ssh "$API_NODE" \
  --project="$GCP_PROJECT" --zone="$GCP_ZONE" -- "
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
for i in \$(seq 1 60); do
  if kubectl get nodes -l infrabox-role=vm-worker --no-headers 2>/dev/null | grep -q Ready; then
    echo '✓ Worker node joined and ready'
    break
  fi
  sleep 10
done
kubectl get nodes -o wide
"

# =============================================================
echo ""
echo "============================================"
echo " InfraBox setup complete!"
echo "============================================"
echo ""
echo " Architecture:"
echo "   API node:    $API_NODE ($API_MACHINE_TYPE, on-demand)"
echo "   Worker MIG:  $WORKER_MIG ($WORKER_MACHINE_TYPE, spot, count=$WORKER_COUNT)"
echo "   Storage:     GCE PD-SSD (CSI driver)"
echo ""
echo " IP:         $STATIC_IP"
echo " Domain:     $DOMAIN  (*.${DOMAIN})"
echo " Admin Key:  $ADMIN_API_KEY"
echo " k3s Token:  $K3S_TOKEN"
echo ""
echo " DNS: Add these records to your DNS provider:"
echo "   A  $DOMAIN       -> $STATIC_IP"
echo "   A  *.$DOMAIN     -> $STATIC_IP"
echo ""
echo " CLI config (~/.ib/config.yaml):"
echo "   endpoint:    https://api.${DOMAIN}"
echo "   sshpiper_ip: ${STATIC_IP}"
echo ""
echo " SSH into nodes:"
echo "   gcloud compute ssh $API_NODE --project=$GCP_PROJECT --zone=$GCP_ZONE"
echo "============================================"
