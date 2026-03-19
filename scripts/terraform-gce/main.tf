# =============================================================
# InfraBox — GCE single-file Terraform
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
}

variable "gcp_zone" {
  description = "GCE zone"
  type        = string
  default     = "asia-northeast1-a"
}

variable "domain" {
  description = "Base domain for InfraBox (e.g. infrabox.example.com)"
  type        = string
}

variable "letsencrypt_email" {
  description = "Email for Let's Encrypt certificate"
  type        = string
}

variable "instance_name" {
  description = "GCE instance name"
  type        = string
  default     = "infrabox-k3s"
}

variable "machine_type" {
  description = "GCE machine type"
  type        = string
  default     = "e2-medium"
}

variable "boot_disk_size" {
  description = "Boot disk size in GB"
  type        = number
  default     = 50
}

variable "spot" {
  description = "Use spot (preemptible) instance"
  type        = bool
  default     = true
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
  description = "Google OAuth2 client secret"
  type        = string
  default     = ""
  sensitive   = true
}

variable "oauth_email_domain" {
  description = "Allowed email domain for OAuth (e.g. example.com)"
  type        = string
  default     = ""
}

# -------------------------------------------------------------
# Locals
# -------------------------------------------------------------

locals {
  region     = join("-", slice(split("-", var.gcp_zone), 0, 2))
  api_key    = var.admin_api_key != "" ? var.admin_api_key : random_password.admin_api_key.result
  auth_domain = "auth.${var.domain}"

  source_cidrs = length(var.allowed_cidrs) > 0 ? var.allowed_cidrs : ["0.0.0.0/0"]
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
# Random admin API key (used when not supplied)
# -------------------------------------------------------------

resource "random_password" "admin_api_key" {
  length  = 32
  special = false
}

# -------------------------------------------------------------
# Static IP
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
  target_tags   = [var.instance_name]
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
  target_tags   = [var.instance_name]
  description   = "InfraBox HTTPS"
}

resource "google_compute_firewall" "allow_ssh" {
  name    = "${var.instance_name}-allow-ssh"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["2222"]
  }

  source_ranges = local.source_cidrs
  target_tags   = [var.instance_name]
  description   = "InfraBox SSH via sshpiper"
}

resource "google_compute_firewall" "allow_api" {
  name    = "${var.instance_name}-allow-api"
  network = "default"

  allow {
    protocol = "tcp"
    ports    = ["30080"]
  }

  source_ranges = local.source_cidrs
  target_tags   = [var.instance_name]
  description   = "InfraBox API"
}

# -------------------------------------------------------------
# GCE Instance
# -------------------------------------------------------------

resource "google_compute_instance" "infrabox" {
  name         = var.instance_name
  machine_type = var.machine_type
  zone         = var.gcp_zone
  tags         = [var.instance_name]

  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2404-lts"
      size  = var.boot_disk_size
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
    preemptible                 = var.spot
    automatic_restart           = var.spot ? false : true
    provisioning_model          = var.spot ? "SPOT" : "STANDARD"
    instance_termination_action = var.spot ? "STOP" : null
  }

  metadata_startup_script = <<-STARTUP
    #!/usr/bin/env bash
    set -euo pipefail
    exec > >(tee /var/log/infrabox-startup.log) 2>&1

    log() { echo "=== $$(date '+%H:%M:%S') $$* ==="; }

    MARKER=/var/lib/infrabox-setup-done
    if [ -f "$$MARKER" ]; then
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

    # =========================================================
    log "1. Install k3s"
    # =========================================================
    curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='--disable=traefik' sh -
    chmod 644 /etc/rancher/k3s/k3s.yaml
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

    for i in $$(seq 1 30); do
      kubectl get nodes &>/dev/null && break
      sleep 5
    done

    # =========================================================
    log "2. Install Docker"
    # =========================================================
    curl -fsSL https://get.docker.com | sh

    # =========================================================
    log "3. Build and import Docker images"
    # =========================================================
    cd /tmp
    apt-get update -qq && apt-get install -y -qq git
    git clone --depth 1 https://github.com/shogomuranushi/infrabox.git infrabox-src || true
    cd infrabox-src

    docker build -t infrabox-base:ubuntu-24.04 -f images/base/Dockerfile images/base/
    docker save infrabox-base:ubuntu-24.04 | k3s ctr images import -

    docker build -t infrabox-api:latest -f api/Dockerfile api/
    docker save infrabox-api:latest | k3s ctr images import -

    # =========================================================
    log "4. Create namespaces"
    # =========================================================
    kubectl create ns infrabox     2>/dev/null || true
    kubectl create ns infrabox-vms 2>/dev/null || true
    kubectl create ns sshpiper     2>/dev/null || true

    # =========================================================
    log "5. Install cert-manager"
    # =========================================================
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml
    kubectl -n cert-manager rollout status deploy/cert-manager --timeout=180s
    kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s

    # =========================================================
    log "6. Install nginx-ingress"
    # =========================================================
    kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.12.0/deploy/static/provider/baremetal/deploy.yaml
    kubectl -n ingress-nginx rollout status deploy/ingress-nginx-controller --timeout=180s

    # =========================================================
    log "7. Install sshpiper"
    # =========================================================
    kubectl apply -f https://raw.githubusercontent.com/tg123/sshpiper/master/hack/kubernetes/sshpiperd.yaml -n infrabox
    kubectl patch deployment sshpiper -n infrabox --type='json' -p='[
      {"op":"replace","path":"/spec/template/spec/containers/0/ports",
       "value":[{"containerPort":2222,"hostPort":2222}]}
    ]' 2>/dev/null || true

    # =========================================================
    log "8. Create secrets"
    # =========================================================
    if ! kubectl get secret sshpiper-upstream-key -n infrabox &>/dev/null; then
      ssh-keygen -t ed25519 -N '' -f /tmp/upstream-key
      kubectl create secret generic sshpiper-upstream-key \
        -n infrabox \
        --from-file=ssh-privatekey=/tmp/upstream-key
      rm -f /tmp/upstream-key /tmp/upstream-key.pub
    fi

    kubectl get secret sshpiper-upstream-key -n infrabox -o json \
      | python3 -c "
    import json,sys
    s=json.load(sys.stdin)
    for k in ['resourceVersion','uid','creationTimestamp']:
      s['metadata'].pop(k, None)
    s['metadata']['namespace']='infrabox-vms'
    print(json.dumps(s))
    " | kubectl apply -f - 2>/dev/null || true

    if ! kubectl get secret sshpiper-server-key -n infrabox &>/dev/null; then
      ssh-keygen -t ed25519 -N '' -f /tmp/server-key
      kubectl create secret generic sshpiper-server-key \
        -n infrabox \
        --from-file=ssh-hostkey=/tmp/server-key
      rm -f /tmp/server-key /tmp/server-key.pub
    fi

    kubectl create secret generic infrabox-api-secret \
      -n infrabox \
      --from-literal=api-key="$$ADMIN_API_KEY" \
      --from-literal=ingress-ip="$$STATIC_IP" \
      --from-literal=sshpiper-ip="$$STATIC_IP" \
      --dry-run=client -o yaml | kubectl apply -f -

    # =========================================================
    log "9. Deploy infrabox-api"
    # =========================================================
    cd /tmp/infrabox-src
    kubectl apply -f k8s/rbac.yaml
    kubectl apply -f k8s/api-deployment.yaml

    AUTH_ENV_ARGS=""
    if [ -n "$$OAUTH_CLIENT_ID" ]; then
      AUTH_ENV_ARGS="INFRABOX_AUTH_URL=https://$$AUTH_DOMAIN"
    fi
    kubectl set env deployment/infrabox-api \
      INFRABOX_INGRESS_DOMAIN="$$DOMAIN" \
      $$AUTH_ENV_ARGS \
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
        email: $$LETSENCRYPT_EMAIL
        privateKeySecretRef:
          name: letsencrypt-account-key
        solvers:
        - http01:
            ingress:
              class: nginx
    EOF

    sed "s/API_DOMAIN_PLACEHOLDER/api.$$DOMAIN/g" k8s/api-ingress.yaml \
      | kubectl apply -f -

    # =========================================================
    log "10. oauth2-proxy (optional)"
    # =========================================================
    if [ -n "$$OAUTH_CLIENT_ID" ]; then
      COOKIE_SECRET=$$(openssl rand -base64 32 | tr -d '\n')

      kubectl create secret generic oauth2-proxy-secret \
        -n infrabox \
        --from-literal=client-id="$$OAUTH_CLIENT_ID" \
        --from-literal=client-secret="$$OAUTH_CLIENT_SECRET" \
        --from-literal=cookie-secret="$$COOKIE_SECRET" \
        --from-literal=email-domain="$$OAUTH_EMAIL_DOMAIN" \
        --from-literal=cookie-domain=".$$DOMAIN" \
        --dry-run=client -o yaml | kubectl apply -f -

      sed "s/AUTH_DOMAIN_PLACEHOLDER/$$AUTH_DOMAIN/g" k8s/oauth2-proxy.yaml \
        | kubectl apply -f -

      sed -e "s/API_DOMAIN_PLACEHOLDER/api.$$DOMAIN/g" \
          -e "s/AUTH_DOMAIN_PLACEHOLDER/$$AUTH_DOMAIN/g" \
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
    touch "$$MARKER"
  STARTUP

  service_account {
    scopes = ["cloud-platform"]
  }

  allow_stopping_for_update = true
}

# -------------------------------------------------------------
# Outputs
# -------------------------------------------------------------

output "static_ip" {
  description = "Static IP address"
  value       = google_compute_address.infrabox.address
}

output "admin_api_key" {
  description = "Admin API key"
  value       = local.api_key
  sensitive   = true
}

output "ssh_command" {
  description = "SSH into the instance"
  value       = "gcloud compute ssh ${var.instance_name} --project=${var.gcp_project} --zone=${var.gcp_zone}"
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
    endpoint:    https://api.${var.domain}
    sshpiper_ip: ${google_compute_address.infrabox.address}
  EOT
}
