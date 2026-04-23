# =============================================================
# InfraBox GKE Standard — Terraform
#
# Architecture:
#   - System node pool: on-demand, e2-standard-2  (nginx, cert-manager, infrabox-api)
#   - VM worker pool:   spot, e2-standard-4, autoscaling (user VM pods)
#   - Storage:         GCE PD-SSD via built-in GKE CSI driver
#   - Images:          ghcr.io/<ghcr_user>/infrabox-*
#
# Usage:
#   cd scripts/terraform-gke
#   terraform init
#   terraform apply \
#     -var="gcp_project=your-project" \
#     -var="domain=infrabox.example.com" \
#     -var="letsencrypt_email=you@example.com"
#
# Or copy terraform.tfvars and run: terraform apply
# =============================================================

terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
    null = {
      source  = "hashicorp/null"
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
  description = "GCP zone"
  type        = string
  default     = "asia-northeast1-b"
}

variable "domain" {
  description = "Base domain for InfraBox (e.g. infrabox.example.com)"
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]+[a-z0-9]$", var.domain))
    error_message = "domain must be a valid domain name."
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

variable "cluster_name" {
  description = "GKE cluster name"
  type        = string
  default     = "infrabox"
}

variable "system_machine_type" {
  description = "Machine type for system node pool (on-demand)"
  type        = string
  default     = "e2-standard-2"
}

variable "worker_machine_type" {
  description = "Machine type for VM worker node pool (spot)"
  type        = string
  default     = "e2-standard-4"
}

variable "worker_min" {
  description = "Minimum worker nodes (autoscaling)"
  type        = number
  default     = 1
}

variable "worker_max" {
  description = "Maximum worker nodes (autoscaling)"
  type        = number
  default     = 10
}

variable "static_ip_name" {
  description = "Name for the regional static IP"
  type        = string
  default     = "infrabox-ip"
}

variable "admin_api_key" {
  description = "Admin API key. Auto-generated if empty."
  type        = string
  default     = ""
  sensitive   = true
}

variable "ghcr_user" {
  description = "GitHub Container Registry username"
  type        = string
  default     = "shogomuranushi"
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
  description = "Allowed email domain for OAuth (e.g. example.com)"
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
  auth_url    = var.oauth_client_id != "" ? "https://auth.${var.domain}" : ""

  system_toleration = [{
    key      = "infrabox-role"
    operator = "Equal"
    value    = "api"
    effect   = "NoSchedule"
  }]
  system_node_selector = { "infrabox-role" = "api" }
}

# -------------------------------------------------------------
# Providers
# -------------------------------------------------------------

provider "google" {
  project = var.gcp_project
  zone    = var.gcp_zone
}

data "google_client_config" "default" {}

provider "kubernetes" {
  host                   = "https://${google_container_cluster.main.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.main.master_auth[0].cluster_ca_certificate)
}

provider "helm" {
  kubernetes {
    host                   = "https://${google_container_cluster.main.endpoint}"
    token                  = data.google_client_config.default.access_token
    cluster_ca_certificate = base64decode(google_container_cluster.main.master_auth[0].cluster_ca_certificate)
  }
}

# -------------------------------------------------------------
# Random values
# -------------------------------------------------------------

resource "random_password" "admin_api_key" {
  length  = 32
  special = false
}

resource "random_password" "cookie_secret" {
  count   = var.oauth_client_id != "" ? 1 : 0
  length  = 32
  special = false
}

# -------------------------------------------------------------
# 1. Enable required GCP APIs
# -------------------------------------------------------------

resource "google_project_service" "container" {
  service            = "container.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "compute" {
  service            = "compute.googleapis.com"
  disable_on_destroy = false
}

# -------------------------------------------------------------
# 2. Static IP (regional)
# -------------------------------------------------------------

resource "google_compute_address" "ingress" {
  name   = var.static_ip_name
  region = local.region
  depends_on = [google_project_service.compute]
}

# -------------------------------------------------------------
# 3. GKE Standard cluster
# -------------------------------------------------------------

resource "google_container_cluster" "main" {
  name     = var.cluster_name
  location = var.gcp_zone

  remove_default_node_pool = true
  initial_node_count       = 1

  release_channel {
    channel = "STABLE"
  }

  ip_allocation_policy {}

  workload_identity_config {
    workload_pool = "${var.gcp_project}.svc.id.goog"
  }

  depends_on = [google_project_service.container]
}

# -------------------------------------------------------------
# 4. Node pools
# -------------------------------------------------------------

# System pool: on-demand, tainted so only infra pods run here
resource "google_container_node_pool" "system" {
  name     = "infrabox-system"
  cluster  = google_container_cluster.main.name
  location = var.gcp_zone

  node_count = 1

  node_config {
    machine_type = var.system_machine_type
    disk_type    = "pd-ssd"
    disk_size_gb = 50

    labels = { "infrabox-role" = "api" }

    taint {
      key    = "infrabox-role"
      value  = "api"
      effect = "NO_SCHEDULE"
    }

    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}

# Worker pool: spot, autoscaling, labeled for VM workloads
resource "google_container_node_pool" "workers" {
  name     = "infrabox-vms"
  cluster  = google_container_cluster.main.name
  location = var.gcp_zone

  autoscaling {
    min_node_count = var.worker_min
    max_node_count = var.worker_max
  }

  node_config {
    machine_type = var.worker_machine_type
    disk_type    = "pd-ssd"
    disk_size_gb = 50
    spot         = true

    labels = { "infrabox-role" = "vm-worker" }

    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]
  }
}

# -------------------------------------------------------------
# 5. Namespaces
# -------------------------------------------------------------

resource "kubernetes_namespace" "infrabox" {
  metadata { name = "infrabox" }
  depends_on = [google_container_node_pool.system]
}

resource "kubernetes_namespace" "infrabox_vms" {
  metadata { name = "infrabox-vms" }
  depends_on = [google_container_node_pool.system]
}

# -------------------------------------------------------------
# 6. nginx-ingress (with static IP, pinned to system node)
# -------------------------------------------------------------

resource "helm_release" "nginx_ingress" {
  name             = "ingress-nginx"
  repository       = "https://kubernetes.github.io/ingress-nginx"
  chart            = "ingress-nginx"
  namespace        = "ingress-nginx"
  create_namespace = true

  values = [yamlencode({
    controller = {
      service = {
        loadBalancerIP = google_compute_address.ingress.address
      }
      nodeSelector = local.system_node_selector
      tolerations  = local.system_toleration
      admissionWebhooks = {
        patch = {
          nodeSelector = local.system_node_selector
          tolerations  = local.system_toleration
        }
      }
    }
  })]

  depends_on = [google_container_node_pool.system]
}

# -------------------------------------------------------------
# 7. cert-manager (pinned to system node)
# -------------------------------------------------------------

resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = "v1.16.2"
  namespace        = "cert-manager"
  create_namespace = true

  set {
    name  = "crds.enabled"
    value = "true"
  }

  values = [yamlencode({
    nodeSelector = local.system_node_selector
    tolerations  = local.system_toleration
    webhook = {
      nodeSelector = local.system_node_selector
      tolerations  = local.system_toleration
    }
    cainjector = {
      nodeSelector = local.system_node_selector
      tolerations  = local.system_toleration
    }
    startupapicheck = {
      nodeSelector = local.system_node_selector
      tolerations  = local.system_toleration
    }
  })]

  depends_on = [google_container_node_pool.system]
}

# ClusterIssuer (Let's Encrypt) — applied via local-exec after cert-manager CRDs are ready
# kubernetes_manifest is avoided here because it requires cluster connectivity at plan time.
resource "null_resource" "cluster_issuer" {
  triggers = {
    letsencrypt_email    = var.letsencrypt_email
    cert_manager_version = helm_release.cert_manager.version
  }

  provisioner "local-exec" {
    command = <<-EOT
      gcloud container clusters get-credentials ${var.cluster_name} \
        --project=${var.gcp_project} --zone=${var.gcp_zone} --quiet
      cat <<'YAML' | kubectl apply -f -
      apiVersion: cert-manager.io/v1
      kind: ClusterIssuer
      metadata:
        name: letsencrypt
      spec:
        acme:
          server: https://acme-v02.api.letsencrypt.org/directory
          email: ${var.letsencrypt_email}
          privateKeySecretRef:
            name: letsencrypt-account-key
          solvers:
          - http01:
              ingress:
                class: nginx
      YAML
    EOT
  }

  depends_on = [helm_release.cert_manager]
}

# -------------------------------------------------------------
# 8. StorageClass (pd-ssd via built-in GKE CSI driver)
# -------------------------------------------------------------

resource "kubernetes_storage_class" "pd_ssd" {
  metadata {
    name = "pd-ssd"
  }
  storage_provisioner    = "pd.csi.storage.gke.io"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true
  parameters = {
    type = "pd-ssd"
  }
  depends_on = [google_container_node_pool.system]
}

# -------------------------------------------------------------
# 9. RBAC
# -------------------------------------------------------------

resource "kubernetes_service_account" "infrabox_api" {
  metadata {
    name      = "infrabox-api"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
}

resource "kubernetes_cluster_role" "infrabox_api" {
  metadata { name = "infrabox-api" }

  rule {
    api_groups = [""]
    resources  = ["namespaces"]
    verbs      = ["create", "get", "list", "delete"]
  }
  rule {
    api_groups = [""]
    resources  = ["resourcequotas"]
    verbs      = ["create", "get", "list", "update", "patch"]
  }
  rule {
    api_groups = ["apps"]
    resources  = ["deployments"]
    verbs      = ["create", "get", "list", "delete", "patch", "update"]
  }
  rule {
    api_groups = [""]
    resources  = ["persistentvolumeclaims", "services", "pods"]
    verbs      = ["create", "get", "list", "delete", "watch"]
  }
  rule {
    api_groups = [""]
    resources  = ["pods/exec"]
    verbs      = ["create"]
  }
  rule {
    api_groups = ["networking.k8s.io"]
    resources  = ["ingresses"]
    verbs      = ["create", "get", "list", "delete", "patch", "update"]
  }
  rule {
    api_groups = [""]
    resources  = ["nodes"]
    verbs      = ["get", "list"]
  }
}

resource "kubernetes_cluster_role_binding" "infrabox_api" {
  metadata { name = "infrabox-api" }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = kubernetes_cluster_role.infrabox_api.metadata[0].name
  }

  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account.infrabox_api.metadata[0].name
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
}

# -------------------------------------------------------------
# 10. Secrets
# -------------------------------------------------------------

resource "kubernetes_secret" "infrabox_api" {
  metadata {
    name      = "infrabox-api-secret"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
  data = {
    "api-key"    = local.api_key
    "ingress-ip" = google_compute_address.ingress.address
  }
}

# -------------------------------------------------------------
# 11. infrabox-api Deployment + Service
# -------------------------------------------------------------

resource "kubernetes_persistent_volume_claim" "infrabox_api_data" {
  metadata {
    name      = "infrabox-api-data"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
  spec {
    storage_class_name = kubernetes_storage_class.pd_ssd.metadata[0].name
    access_modes       = ["ReadWriteOnce"]
    resources {
      requests = { storage = "1Gi" }
    }
  }
  wait_until_bound = false
}

resource "kubernetes_deployment" "infrabox_api" {
  metadata {
    name      = "infrabox-api"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }

  spec {
    replicas = 1

    selector {
      match_labels = { app = "infrabox-api" }
    }

    template {
      metadata {
        labels = { app = "infrabox-api" }
      }

      spec {
        service_account_name = kubernetes_service_account.infrabox_api.metadata[0].name

        toleration {
          key      = "infrabox-role"
          operator = "Equal"
          value    = "api"
          effect   = "NoSchedule"
        }

        node_selector = local.system_node_selector

        container {
          name              = "api"
          image             = "ghcr.io/${var.ghcr_user}/infrabox-api:latest"
          image_pull_policy = "Always"

          port { container_port = 8080 }

          env {
            name = "INFRABOX_API_KEY"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.infrabox_api.metadata[0].name
                key  = "api-key"
              }
            }
          }
          env {
            name = "INFRABOX_INGRESS_IP"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.infrabox_api.metadata[0].name
                key  = "ingress-ip"
              }
            }
          }
          env {
            name  = "INFRABOX_INGRESS_DOMAIN"
            value = var.domain
          }
          env {
            name  = "INFRABOX_STORAGE_CLASS"
            value = "pd-ssd"
          }
          env {
            name  = "INFRABOX_VM_NODE_SELECTOR"
            value = "infrabox-role=vm-worker"
          }
          env {
            name  = "INFRABOX_BASE_IMAGE"
            value = "ghcr.io/${var.ghcr_user}/infrabox-base:ubuntu-24.04"
          }
          env {
            name  = "INFRABOX_AUTH_URL"
            value = local.auth_url
          }

          volume_mount {
            name       = "data"
            mount_path = "/data"
          }
        }

        volume {
          name = "data"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim.infrabox_api_data.metadata[0].name
          }
        }
      }
    }
  }

  depends_on = [
    kubernetes_secret.infrabox_api,
    helm_release.nginx_ingress,
    helm_release.cert_manager,
  ]
}

resource "kubernetes_service" "infrabox_api" {
  metadata {
    name      = "infrabox-api"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
  spec {
    selector = { app = "infrabox-api" }
    type     = "ClusterIP"
    port {
      port        = 8080
      target_port = "8080"
    }
  }
}

# -------------------------------------------------------------
# 12. API Ingress
# -------------------------------------------------------------

resource "kubernetes_ingress_v1" "infrabox_api" {
  metadata {
    name      = "infrabox-api-ingress"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
    annotations = {
      "cert-manager.io/cluster-issuer"                    = "letsencrypt"
      "nginx.ingress.kubernetes.io/proxy-body-size"        = "200m"
      "nginx.ingress.kubernetes.io/proxy-read-timeout"     = "3600"
      "nginx.ingress.kubernetes.io/proxy-send-timeout"     = "3600"
    }
  }

  spec {
    ingress_class_name = "nginx"

    tls {
      hosts       = ["api.${var.domain}"]
      secret_name = "tls-infrabox-api"
    }

    rule {
      host = "api.${var.domain}"
      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend {
            service {
              name = kubernetes_service.infrabox_api.metadata[0].name
              port { number = 8080 }
            }
          }
        }
      }
    }
  }

  depends_on = [null_resource.cluster_issuer]
}

# -------------------------------------------------------------
# 13. oauth2-proxy (optional — only if oauth_client_id is set)
# -------------------------------------------------------------

resource "kubernetes_secret" "oauth2_proxy" {
  count = var.oauth_client_id != "" ? 1 : 0

  metadata {
    name      = "oauth2-proxy-secret"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
  data = {
    "client-id"     = var.oauth_client_id
    "client-secret" = var.oauth_client_secret
    "cookie-secret" = random_password.cookie_secret[0].result
    "email-domain"  = var.oauth_email_domain
    "cookie-domain" = ".${var.domain}"
  }
}

resource "kubernetes_deployment" "oauth2_proxy" {
  count = var.oauth_client_id != "" ? 1 : 0

  metadata {
    name      = "oauth2-proxy"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }

  spec {
    replicas = 1

    selector {
      match_labels = { app = "oauth2-proxy" }
    }

    template {
      metadata {
        labels = { app = "oauth2-proxy" }
      }

      spec {
        toleration {
          key      = "infrabox-role"
          operator = "Equal"
          value    = "api"
          effect   = "NoSchedule"
        }

        node_selector = local.system_node_selector

        container {
          name  = "oauth2-proxy"
          image = "quay.io/oauth2-proxy/oauth2-proxy:v7.6.0"

          args = [
            "--provider=google",
            "--upstream=file:///dev/null",
            "--http-address=0.0.0.0:4180",
            "--reverse-proxy=true",
            "--skip-provider-button=true",
            "--cookie-secure=true",
            "--cookie-samesite=lax",
          ]

          env {
            name = "OAUTH2_PROXY_CLIENT_ID"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "client-id"
              }
            }
          }
          env {
            name = "OAUTH2_PROXY_CLIENT_SECRET"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "client-secret"
              }
            }
          }
          env {
            name = "OAUTH2_PROXY_COOKIE_SECRET"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "cookie-secret"
              }
            }
          }
          env {
            name = "OAUTH2_PROXY_EMAIL_DOMAINS"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "email-domain"
              }
            }
          }
          env {
            name = "OAUTH2_PROXY_COOKIE_DOMAINS"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "cookie-domain"
              }
            }
          }
          env {
            name = "OAUTH2_PROXY_WHITELIST_DOMAINS"
            value_from {
              secret_key_ref {
                name = kubernetes_secret.oauth2_proxy[0].metadata[0].name
                key  = "cookie-domain"
              }
            }
          }

          port { container_port = 4180 }
        }
      }
    }
  }

  depends_on = [kubernetes_secret.oauth2_proxy]
}

resource "kubernetes_service" "oauth2_proxy" {
  count = var.oauth_client_id != "" ? 1 : 0

  metadata {
    name      = "oauth2-proxy-svc"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
  }
  spec {
    selector = { app = "oauth2-proxy" }
    port {
      port        = 4180
      target_port = "4180"
    }
  }
}

resource "kubernetes_ingress_v1" "oauth2_proxy" {
  count = var.oauth_client_id != "" ? 1 : 0

  metadata {
    name      = "oauth2-proxy-ingress"
    namespace = kubernetes_namespace.infrabox.metadata[0].name
    annotations = {
      "cert-manager.io/cluster-issuer" = "letsencrypt"
    }
  }

  spec {
    ingress_class_name = "nginx"

    tls {
      hosts       = [local.auth_domain]
      secret_name = "tls-oauth2-proxy"
    }

    rule {
      host = local.auth_domain
      http {
        path {
          path      = "/oauth2"
          path_type = "Prefix"
          backend {
            service {
              name = kubernetes_service.oauth2_proxy[0].metadata[0].name
              port { number = 4180 }
            }
          }
        }
      }
    }
  }

  depends_on = [null_resource.cluster_issuer]
}

# -------------------------------------------------------------
# Outputs
# -------------------------------------------------------------

output "static_ip" {
  description = "Static IP address for DNS configuration"
  value       = google_compute_address.ingress.address
}

output "admin_api_key" {
  description = "Admin API key for CLI configuration"
  value       = local.api_key
  sensitive   = true
}

output "cluster_name" {
  description = "GKE cluster name"
  value       = google_container_cluster.main.name
}

output "dns_instructions" {
  description = "DNS records to add to your DNS provider"
  value       = <<-EOT
    Add these A records to your DNS provider:
      ${var.domain}   -> ${google_compute_address.ingress.address}
      *.${var.domain} -> ${google_compute_address.ingress.address}
  EOT
}

output "cli_config" {
  description = "CLI configuration (~/.ib/config.yaml)"
  value       = "endpoint: https://api.${var.domain}"
}
