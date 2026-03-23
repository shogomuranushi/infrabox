# =============================================================
# InfraBox — GCE two-node Terraform
#
# Architecture:
#   - API node:    e2-small, on-demand, k3s server + control plane workloads
#   - Worker node: n2d-standard-4, spot, MIG-managed, k3s agent + VM workloads
#   - Storage:     GCE PD via CSI driver (persistent across spot preemption)
#
# Usage:
#   cd scripts/terraform-gce
#   terraform init
#   terraform apply \
#     -var="gcp_project=your-project" \
#     -var="domain=infrabox.example.com" \
#     -var="letsencrypt_email=you@example.com"
# =============================================================

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

# -------------------------------------------------------------
# Variables
# -------------------------------------------------------------

variable "gcp_project" {
  description = "GCP project ID"
  type        = string

  validation {
    condition     = length(var.gcp_project) > 0
    error_message = "gcp_project must not be empty."
  }
}

variable "gcp_zone" {
  description = "GCE zone"
  type        = string
  default     = "asia-northeast1-a"
}

variable "domain" {
  description = "Base domain for InfraBox (e.g. infrabox.example.com)"
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.domain))
    error_message = "domain must be a valid domain name (e.g. infrabox.example.com)."
  }
}

variable "letsencrypt_email" {
  description = "Email for Let's Encrypt certificate"
  type        = string

  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.[^@]+$", var.letsencrypt_email))
    error_message = "letsencrypt_email must be a valid email address."
  }
}

variable "instance_name" {
  description = "Base name for GCE resources"
  type        = string
  default     = "infrabox"
}

variable "api_machine_type" {
  description = "Machine type for API node (on-demand)"
  type        = string
  default     = "e2-small"
}

variable "worker_machine_type" {
  description = "Machine type for worker node (spot)"
  type        = string
  default     = "n2d-standard-4"
}

variable "api_disk_size" {
  description = "API node boot disk size in GB"
  type        = number
  default     = 20
}

variable "worker_disk_size" {
  description = "Worker node boot disk size in GB"
  type        = number
  default     = 20
}

variable "worker_count" {
  description = "Number of worker instances in MIG"
  type        = number
  default     = 1
}

variable "allowed_cidrs" {
  description = "List of CIDRs allowed to access SSH/HTTPS/API. Empty = open to all."
  type        = list(string)
  default     = []
}

variable "admin_api_key" {
  description = "Admin API key. Auto-generated if empty."
  type        = string
  default     = ""
  sensitive   = true
}

variable "oauth_client_id" {
  description = "Google OAuth2 client ID (optional, enables Google Workspace auth)"
  type        = string
  default     = ""
}

variable "oauth_client_secret" {
  description = "Google OAuth2 client secret (required if oauth_client_id is set)"
  type        = string
  default     = ""
  sensitive   = true
}

variable "oauth_email_domain" {
  description = "Allowed email domain for OAuth (e.g. example.com, required if oauth_client_id is set)"
  type        = string
  default     = ""
}

# -------------------------------------------------------------
# Locals
# -------------------------------------------------------------

locals {
  region      = join("-", slice(split("-", var.gcp_zone), 0, 2))
  api_key     = var.admin_api_key != "" ? var.admin_api_key : random_password.admin_api_key.result
  auth_domain = "auth.${var.domain}"

  source_cidrs = length(var.allowed_cidrs) > 0 ? var.allowed_cidrs : ["0.0.0.0/0"]

  k3s_token = random_password.k3s_token.result
}

# -------------------------------------------------------------
# Provider
# -------------------------------------------------------------

provider "google" {
  project = var.gcp_project
  region  = local.region
  zone    = var.gcp_zone
}

# -------------------------------------------------------------
# Random secrets
# -------------------------------------------------------------

resource "random_password" "admin_api_key" {
  length  = 32
  special = false
}

resource "random_password" "k3s_token" {
  length  = 64
  special = false
}

# -------------------------------------------------------------
# Static IP (for API node)
# -------------------------------------------------------------

resource "google_compute_address" "infrabox" {
  name   = "${var.instance_name}-ip"
  region = local.region
}

# -------------------------------------------------------------
# Firewall rules
# -------------------------------------------------------------

resource "google_compute_firewall" "allow_health" {
  name    = "${var.instance_name}-allow-health"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["80"]
  }

  source_ranges = ["130.211.0.0/22", "35.191.0.0/16"]
  target_tags   = ["${var.instance_name}-api"]
  description   = "Let's Encrypt HTTP-01 challenge"
}

resource "google_compute_firewall" "allow_https" {
  name    = "${var.instance_name}-allow-https"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["443", "80"]
  }

  source_ranges = local.source_cidrs
  target_tags   = ["${var.instance_name}-api"]
  description   = "InfraBox HTTPS"
}

resource "google_compute_firewall" "allow_internal" {
  name    = "${var.instance_name}-allow-internal"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["6443", "10250", "8472", "51820", "51821"]
  }

  allow {
    protocol = "udp"
    ports    = ["8472", "51820", "51821"]
  }

  source_tags = ["${var.instance_name}-api", "${var.instance_name}-worker"]
  target_tags = ["${var.instance_name}-api", "${var.instance_name}-worker"]
  description = "k3s internal communication (API server, kubelet, flannel VXLAN/WireGuard)"
}

resource "google_compute_firewall" "allow_mig_health" {
  name    = "${var.instance_name}-allow-mig-health"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["10256"]
  }

  source_ranges = ["130.211.0.0/22", "35.191.0.0/16"]
  target_tags   = ["${var.instance_name}-worker"]
  description   = "MIG autohealing health check"
}

# -------------------------------------------------------------
# API Node (on-demand, k3s server)
# -------------------------------------------------------------

resource "google_compute_instance" "api" {
  name         = "${var.instance_name}-api"
  machine_type = var.api_machine_type
  zone         = var.gcp_zone
  tags         = ["${var.instance_name}-api"]

  lifecycle {
    precondition {
      condition     = var.oauth_client_id == "" || (var.oauth_client_secret != "" && var.oauth_email_domain != "")
      error_message = "oauth_client_secret and oauth_email_domain are required when oauth_client_id is set."
    }
  }

  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
      size  = var.api_disk_size
      type  = "pd-ssd"
    }
  }

  network_interface {
    network = "default"
    access_config {
      nat_ip = google_compute_address.infrabox.address
    }
  }

  scheduling {
    preemptible       = false
    automatic_restart = true
    provisioning_model = "STANDARD"
  }

  metadata_startup_script = <<-STARTUP
    #!/usr/bin/env bash
    set -euo pipefail
    exec > >(tee /var/log/infrabox-startup.log) 2>&1

    log() { echo "=== $(date '+%H:%M:%S') $* ==="; }

    MARKER=/var/lib/infrabox-setup-done
    if [ -f "$MARKER" ]; then
      echo "InfraBox setup already completed. Skipping."
      exit 0
    fi

    DOMAIN="${var.domain}"
    LETSENCRYPT_EMAIL="${var.letsencrypt_email}"
    ADMIN_API_KEY="${local.api_key}"
    STATIC_IP="${google_compute_address.infrabox.address}"
    OAUTH_CLIENT_ID="${var.oauth_client_id}"
    OAUTH_CLIENT_SECRET="${var.oauth_client_secret}"
    OAUTH_EMAIL_DOMAIN="${var.oauth_email_domain}"
    AUTH_DOMAIN="${local.auth_domain}"
    K3S_TOKEN="${local.k3s_token}"

    # =========================================================
    log "1. Install k3s server"
    # =========================================================
    curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='--disable=traefik --node-taint infrabox-role=api:NoSchedule --node-label infrabox-role=api' K3S_TOKEN="$K3S_TOKEN" sh -
    chmod 644 /etc/rancher/k3s/k3s.yaml
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

    for i in $(seq 1 30); do
      kubectl get nodes &>/dev/null && break
      sleep 5
    done

    # Patch kube-system deployments to tolerate the API node taint
    sleep 30
    for deploy in coredns local-path-provisioner metrics-server; do
      kubectl patch deployment "$deploy" -n kube-system --type='json' -p='[
        {"op":"add","path":"/spec/template/spec/tolerations",
         "value":[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]}
      ]' 2>/dev/null || true
    done

    # =========================================================
    log "2. Install Docker"
    # =========================================================
    curl -fsSL https://get.docker.com | sh

    # =========================================================
    log "3. Pull Docker images from GHCR"
    # =========================================================
    docker pull ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04
    docker tag ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04 infrabox-base:ubuntu-24.04
    docker save infrabox-base:ubuntu-24.04 | k3s ctr images import -

    docker pull ghcr.io/shogomuranushi/infrabox-api:latest
    docker tag ghcr.io/shogomuranushi/infrabox-api:latest infrabox-api:latest
    docker save infrabox-api:latest | k3s ctr images import -

    # =========================================================
    log "4. Create namespaces"
    # =========================================================
    kubectl create ns infrabox     2>/dev/null || true
    kubectl create ns infrabox-vms 2>/dev/null || true

    # =========================================================
    log "5. Install cert-manager"
    # =========================================================
    command -v helm &>/dev/null || curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
    helm repo add jetstack https://charts.jetstack.io --force-update
    helm upgrade --install cert-manager jetstack/cert-manager \
      --namespace cert-manager --create-namespace \
      --version v1.16.2 \
      --set crds.enabled=true \
      --set-json 'tolerations=[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]' \
      --set-json 'webhook.tolerations=[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]' \
      --set-json 'cainjector.tolerations=[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]' \
      --set-json 'startupapicheck.tolerations=[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]' \
      --wait --timeout 5m

    # =========================================================
    log "6. Install nginx-ingress"
    # =========================================================
    helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx --force-update
    helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
      --namespace ingress-nginx --create-namespace \
      --set controller.hostPort.enabled=true \
      --set controller.service.type=ClusterIP \
      --set controller.admissionWebhooks.enabled=false \
      --set-json 'controller.tolerations=[{"key":"infrabox-role","operator":"Equal","value":"api","effect":"NoSchedule"}]' \
      --wait --timeout 5m

    # =========================================================
    log "7. Install GCE PD CSI Driver"
    # =========================================================
    # Download tarball (kubectl apply -k with ?ref= URL not supported by kustomize v5)
    rm -rf /tmp/gcp-csi-driver && mkdir /tmp/gcp-csi-driver
    curl -sL https://github.com/kubernetes-sigs/gcp-compute-persistent-disk-csi-driver/archive/refs/tags/v1.15.1.tar.gz \
      | tar xz -C /tmp/gcp-csi-driver --strip-components=1
    # cloud-sa secret required by CSI driver (GCE workload identity handles actual auth)
    kubectl create namespace gce-pd-csi-driver 2>/dev/null || true
    # cloud-sa secret is required in both kube-system (node daemonset) and
    # gce-pd-csi-driver (controller deployment) namespaces
    for ns in kube-system gce-pd-csi-driver; do
      kubectl create secret generic cloud-sa -n "$ns" \
        --from-literal=cloud-sa.json='{}' \
        --dry-run=client -o yaml | kubectl apply -f -
    done
    # Create a kustomize overlay that adds tolerations declaratively
    mkdir -p /tmp/gcp-csi-overlay
    cat > /tmp/gcp-csi-overlay/kustomization.yaml << 'KUST'
resources:
  - ../gcp-csi-driver/deploy/kubernetes/overlays/stable-master
patches:
  - patch: |
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: csi-gce-pd-controller
        namespace: gce-pd-csi-driver
      spec:
        template:
          spec:
            tolerations:
              - key: infrabox-role
                operator: Equal
                value: api
                effect: NoSchedule
  - patch: |
      apiVersion: apps/v1
      kind: DaemonSet
      metadata:
        name: csi-gce-pd-node
        namespace: gce-pd-csi-driver
      spec:
        template:
          spec:
            tolerations:
              - key: infrabox-role
                operator: Equal
                value: api
                effect: NoSchedule
  KUST
    kubectl apply -k /tmp/gcp-csi-overlay/

    # Wait for CSI driver to be ready
    for i in $(seq 1 30); do
      kubectl get csidrivers pd.csi.storage.gke.io &>/dev/null && break
      sleep 5
    done

    # Create StorageClass for PD-SSD
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

    # =========================================================
    log "8. Create secrets"
    # =========================================================
    kubectl create secret generic infrabox-api-secret \
      -n infrabox \
      --from-literal=api-key="$ADMIN_API_KEY" \
      --from-literal=ingress-ip="$STATIC_IP" \
      --dry-run=client -o yaml | kubectl apply -f -

    # =========================================================
    log "9. Deploy infrabox-api"
    # =========================================================
    cd /tmp/infrabox-src
    kubectl apply -f k8s/rbac.yaml
    kubectl apply -f k8s/api-deployment.yaml

    # Pin API pod to the API node (toleration is declared in api-deployment.yaml)
    kubectl patch deployment infrabox-api -n infrabox --type='json' -p='[
      {"op":"add","path":"/spec/template/spec/nodeSelector",
       "value":{"infrabox-role":"api"}}
    ]'

    AUTH_ENV_ARGS=""
    if [ -n "$OAUTH_CLIENT_ID" ]; then
      AUTH_ENV_ARGS="INFRABOX_AUTH_URL=https://$AUTH_DOMAIN"
    fi
    kubectl set env deployment/infrabox-api \
      INFRABOX_INGRESS_DOMAIN="$DOMAIN" \
      INFRABOX_STORAGE_CLASS="pd-ssd" \
      INFRABOX_VM_NODE_SELECTOR="infrabox-role=vm-worker" \
      INFRABOX_BASE_IMAGE="infrabox-base:ubuntu-24.04" \
      $AUTH_ENV_ARGS \
      -n infrabox

    kubectl rollout status deployment/infrabox-api -n infrabox --timeout=90s

    cat <<EOF | kubectl apply -f -
    apiVersion: cert-manager.io/v1
    kind: ClusterIssuer
    metadata:
      name: letsencrypt
    spec:
      acme:
        server: https://acme-v02.api.letsencrypt.org/directory
        email: $LETSENCRYPT_EMAIL
        privateKeySecretRef:
          name: letsencrypt-account-key
        solvers:
        - http01:
            ingress:
              class: nginx
              podTemplate:
                spec:
                  tolerations:
                  - key: infrabox-role
                    operator: Equal
                    value: api
                    effect: NoSchedule
  EOF

    sed "s/API_DOMAIN_PLACEHOLDER/api.$DOMAIN/g" k8s/api-ingress.yaml \
      | kubectl apply -f -

    # =========================================================
    log "10. oauth2-proxy (optional)"
    # =========================================================
    if [ -n "$OAUTH_CLIENT_ID" ]; then
      COOKIE_SECRET=$(openssl rand -base64 32 | tr -d '\n')

      kubectl create secret generic oauth2-proxy-secret \
        -n infrabox \
        --from-literal=client-id="$OAUTH_CLIENT_ID" \
        --from-literal=client-secret="$OAUTH_CLIENT_SECRET" \
        --from-literal=cookie-secret="$COOKIE_SECRET" \
        --from-literal=email-domain="$OAUTH_EMAIL_DOMAIN" \
        --from-literal=cookie-domain=".$DOMAIN" \
        --dry-run=client -o yaml | kubectl apply -f -

      sed "s/AUTH_DOMAIN_PLACEHOLDER/$AUTH_DOMAIN/g" k8s/oauth2-proxy.yaml \
        | kubectl apply -f -

      sed -e "s/API_DOMAIN_PLACEHOLDER/api.$DOMAIN/g" \
          -e "s/AUTH_DOMAIN_PLACEHOLDER/$AUTH_DOMAIN/g" \
          k8s/api-ingress-auth.yaml \
        | kubectl apply -f -

      kubectl rollout status deployment/oauth2-proxy -n infrabox --timeout=90s
      log "oauth2-proxy deployed"
    else
      log "oauth2-proxy skipped (no OAUTH_CLIENT_ID)"
    fi

    # =========================================================
    log "Setup complete!"
    # =========================================================
    touch "$MARKER"
  STARTUP

  service_account {
    scopes = ["cloud-platform"]
  }

  allow_stopping_for_update = true
}

# -------------------------------------------------------------
# Worker Instance Template (spot, k3s agent)
# -------------------------------------------------------------

resource "google_compute_instance_template" "worker" {
  name_prefix  = "${var.instance_name}-worker-"
  machine_type = var.worker_machine_type
  tags         = ["${var.instance_name}-worker"]

  lifecycle {
    create_before_destroy = true
  }

  disk {
    source_image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
    disk_size_gb = var.worker_disk_size
    disk_type    = "pd-ssd"
    auto_delete  = true
    boot         = true
  }

  network_interface {
    network = "default"
    access_config {} # ephemeral public IP for outbound access
  }

  scheduling {
    preemptible                 = true
    automatic_restart           = false
    provisioning_model          = "SPOT"
    instance_termination_action = "STOP"
  }

  metadata = {
    startup-script = <<-WORKER_STARTUP
      #!/usr/bin/env bash
      set -euo pipefail
      exec > >(tee /var/log/infrabox-worker-startup.log) 2>&1

      log() { echo "=== $(date '+%H:%M:%S') $* ==="; }

      K3S_TOKEN="${local.k3s_token}"
      # Use internal IP for API server (external IP port 6443 is not exposed via firewall)
      API_IP="${google_compute_instance.api.network_interface[0].network_ip}"

      # =========================================================
      log "1. Install k3s agent"
      # =========================================================
      curl -sfL https://get.k3s.io | K3S_URL="https://$API_IP:6443" K3S_TOKEN="$K3S_TOKEN" INSTALL_K3S_EXEC='--node-label=infrabox-role=vm-worker' sh -

      for i in $(seq 1 30); do
        k3s kubectl get nodes &>/dev/null && break
        sleep 5
      done

      # =========================================================
      log "2. Install Docker (for image building)"
      # =========================================================
      curl -fsSL https://get.docker.com | sh

      # =========================================================
      log "3. Pull base image from GHCR"
      # =========================================================
      docker pull ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04
      docker tag ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04 infrabox-base:ubuntu-24.04
      docker save infrabox-base:ubuntu-24.04 | k3s ctr images import -

      log "Worker setup complete!"
    WORKER_STARTUP
  }

  service_account {
    scopes = ["cloud-platform"]
  }
}

# -------------------------------------------------------------
# Worker MIG (Managed Instance Group)
# -------------------------------------------------------------

resource "google_compute_health_check" "worker" {
  name                = "${var.instance_name}-worker-hc"
  check_interval_sec  = 30
  timeout_sec         = 10
  healthy_threshold   = 2
  unhealthy_threshold = 3

  tcp_health_check {
    port = 10250 # kubelet port
  }
}

resource "google_compute_instance_group_manager" "worker" {
  name               = "${var.instance_name}-worker-mig"
  base_instance_name = "${var.instance_name}-worker"
  zone               = var.gcp_zone
  target_size        = var.worker_count

  version {
    instance_template = google_compute_instance_template.worker.self_link
  }

  auto_healing_policies {
    health_check      = google_compute_health_check.worker.id
    initial_delay_sec = 300
  }

  update_policy {
    type                  = "PROACTIVE"
    minimal_action        = "REPLACE"
    max_surge_fixed       = 1
    max_unavailable_fixed = 0
  }
}

# -------------------------------------------------------------
# Outputs
# -------------------------------------------------------------

output "static_ip" {
  description = "Static IP address (API node)"
  value       = google_compute_address.infrabox.address
}

output "admin_api_key" {
  description = "Admin API key"
  value       = local.api_key
  sensitive   = true
}

output "k3s_token" {
  description = "k3s cluster token"
  value       = local.k3s_token
  sensitive   = true
}

output "ssh_command_api" {
  description = "SSH into the API node"
  value       = "gcloud compute ssh ${var.instance_name}-api --project=${var.gcp_project} --zone=${var.gcp_zone}"
}

output "dns_records" {
  description = "DNS records to configure"
  value       = <<-EOT
    A  ${var.domain}    -> ${google_compute_address.infrabox.address}
    A  *.${var.domain}  -> ${google_compute_address.infrabox.address}
  EOT
}

output "cli_config" {
  description = "CLI configuration (~/.ib/config.yaml)"
  value       = <<-EOT
    endpoint: https://api.${var.domain}
  EOT
}
