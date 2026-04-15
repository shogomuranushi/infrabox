# InfraBox

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23+-00ADD8.svg)](https://go.dev/)

**Get a Linux machine in 10 seconds.**

A self-hosted, lightweight VM platform for engineers.
Inspired by [exe.dev](https://exe.dev) — with deep respect for their work.

[日本語版はこちら](./README.ja.md)

---

## What is InfraBox?

InfraBox gives every engineer their own Linux machine in seconds.

```bash
$ ib create my-app
Ready (7s)

  Shell: ib ssh my-app
  URL:   https://my-app.infra.example.com
```

- **Instant web publishing** — every VM gets a public HTTPS URL out of the box
- **Always-on** — perfect for cron jobs, Slack bots, and background services
- **AI-ready** — Claude Code and Codex are pre-installed; start coding with an agent immediately
- **Secure by default** — each VM is fully isolated; experiments never affect each other
- **Resource-efficient** — unlike traditional VMs that lock in fixed resources, containers stay minimal when idle and scale up automatically when in use — fitting far more environments on the same infrastructure

Unlike enterprise-grade dev environment platforms, InfraBox is intentionally thin.
Just Kubernetes + a handful of OSS components.

---

## Features

| | Feature |
|---|---|
| 🖥️ | VM create / list / delete / restart / rename |
| 🔌 | Shell access via WebSocket (no SSH key management needed) |
| 📂 | File transfer (upload / download via API) |
| 🌐 | HTTPS URL auto-provisioning |
| 🔒 | Private / Public / External sharing |
| 🔐 | Google Workspace & Entra ID SSO |
| 🎟️ | Invitation code system for open-mode registration |
| 🛡️ | Per-user namespace isolation & ResourceQuota |
| 💾 | Persistent disk (PVC) |
| 📁 | Google Drive context sharing via rclone |
| 🔑 | Per-VM oauth2 auth toggle (enable / disable per endpoint) |
| 📊 | Resource usage visualization (`ib top` / `ib admin top`) |
| 📜 | First-boot setup script per user (`ib setup-script`) |
| 🔄 | Auto-sync local files to every new VM (`ib sync`) |
| 🔗 | Claude Code SSH integration (`ib ssh-proxy`) |
| 📦 | `ib` CLI tool |

---

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                        User                          │
│   ib ssh my-app       (WebSocket over HTTPS)         │
│   https://my-app.infra.example.com                   │
└──────────────────────┬───────────────────────────────┘
                       │ HTTPS:443
                       ▼
┌────────────────────────────────────────────────────────┐
│              Kubernetes Cluster                         │
│                                                        │
│  API Node (on-demand)                                  │
│  ┌──────────────────────────────────────────────────┐  │
│  │  InfraBox API ── K8s exec (SPDY) ──▶ VM Pods    │  │
│  │  nginx-ingress + cert-manager                    │  │
│  └──────────────────────────────────────────────────┘  │
│                                                        │
│  Worker Node (spot)                                    │
│  ┌──────────────────────────────────────────────────┐  │
│  │  VM Pods (per-user namespace + ResourceQuota)    │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐         │  │
│  │  │ my-app   │ │ demo-env │ │agent-01  │  ...    │  │
│  │  │ Ubuntu   │ │ Ubuntu   │ │ Ubuntu   │         │  │
│  │  │ PD:8GB   │ │ PD:8GB   │ │ PD:8GB   │         │  │
│  │  └──────────┘ └──────────┘ └──────────┘         │  │
│  └──────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────┘
```

### How Shell Access Works

InfraBox uses **WebSocket + K8s exec** instead of SSH:

```
ib ssh myvm
  → WebSocket (wss://api.example.com/v1/vms/myvm/exec)
  → API server authenticates via API key
  → K8s pod exec (SPDY) to the VM container
  → Interactive bash session
```

This means:
- **No SSH keys to manage** — only an API key is needed
- **No SSH port (2222) exposed** — all traffic goes through HTTPS (443)
- **No sshpiper or SSH proxy** — fewer moving parts

### Claude Code SSH Integration

`ib ssh-proxy` acts as an SSH server on stdin/stdout and bridges connections to the WebSocket exec endpoint — allowing tools that require real SSH (like the Claude Code desktop app) to connect directly to VMs.

```
Claude Code ──[SSH protocol]──▶ ib ssh-proxy ──[WebSocket]──▶ VM pod
```

Add to `~/.ssh/config`:

```
Host infrabox-*
  User ubuntu
  ProxyCommand ib ssh-proxy %h
```

Then add an SSH connection in Claude Code with host `ubuntu@infrabox-<vmname>`.

### OSS Stack

| Component | OSS | Role |
|---|---|---|
| HTTPS Proxy | [ingress-nginx](https://github.com/kubernetes/ingress-nginx) + [cert-manager](https://github.com/cert-manager/cert-manager) | TLS termination, wildcard cert |
| SSO | [oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) | Google Workspace / Entra ID auth |
| VM Management | InfraBox API (Go) | VM CRUD, exec, file transfer, quota |

---

## Getting Started

### For Users

#### 1. Install ib CLI

```bash
curl -fsSL https://github.com/shogomuranushi/infrabox/releases/latest/download/install.sh | sudo sh
```

Or download the binary directly from [Releases](https://github.com/shogomuranushi/infrabox/releases).

#### 2. Set up (requires an invitation code from your admin)

```bash
ib init
```

```
Endpoint [https://api.infrabox.example.com]:
Name (e.g. your email): you@example.com
Invitation code: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx

✓ Setup complete. Run 'ib create <name>' to create a VM.
```

#### 3. Create your first VM

```bash
ib create my-app
```

```
Ready (7s)

  Shell:     ib ssh my-app
  HTTPS URL: https://my-app.infra.example.com
```

#### CLI Reference

```bash
ib create my-app           # Create a VM
ib list                    # List your VMs
ib ssh my-app              # Open a shell in the VM (logs in as ubuntu)
ib scp ./file myvm:/tmp/   # Upload a file to the VM
ib scp myvm:/tmp/f ./      # Download a file from the VM
ib rename old new          # Rename a VM
ib delete my-app           # Delete a VM
ib auth enable my-app      # Enable oauth2 auth on the VM's HTTPS endpoint
ib auth disable my-app     # Disable auth (fully open)
ib top                     # Show your resource usage (CPU / memory / VMs)
ib upgrade                 # Upgrade the CLI to the latest version

# Setup script: run a shell script automatically on first VM boot
ib setup-script set ./setup.sh  # Register a setup script
ib setup-script show            # Show current setup script
ib setup-script delete          # Remove setup script

# Auto-sync: transfer local files/dirs to every new VM on creation
ib sync add ~/.claude/settings.json /home/ubuntu/.claude/  # dst = directory
ib sync add ~/.claude.json /home/ubuntu/.claude.json       # dst = exact file path
ib sync list                                               # List sync entries
ib sync remove ~/.claude/settings.json                     # Remove an entry
ib sync now my-app                                         # Sync to existing VM

# Claude Code SSH integration
# Add to ~/.ssh/config:
#   Host infrabox-*
#     User ubuntu
#     ProxyCommand ib ssh-proxy %h
# Then connect from Claude Code with: ubuntu@infrabox-<vmname>
ib ssh-proxy <name>        # ProxyCommand bridge (spawned automatically by SSH)
```

---

### For Admins

#### 1. Deploy the server

```bash
cd scripts/terraform-gke
cp terraform.tfvars.example terraform.tfvars  # fill in your values
terraform init
terraform apply
```

Required variables: `project_id`, `domain`, `letsencrypt_email`.
See [scripts/terraform-gke/](./scripts/terraform-gke/) for full options.

#### 2. Save your admin API key

```bash
ib admin init
# Admin API key: <value from terraform output admin_api_key>
```

#### 3. Issue invitation codes for users

```bash
# Create a single-use invitation code
ib admin invite create

# List all issued codes
ib admin invite list
```

Share the generated code with your user — they enter it during `ib init`.

#### 4. Monitor cluster resource usage

```bash
ib admin top
```

```
╔═══════════════════════════════════════════════════════════════════╗
║                     InfraBox Cluster Status                      ║
╠═══════════════════════════════════════════════════════════════════╣

  VM Worker Nodes (2)
  ──────────────────────────────────────────────────────────────
  gke-worker-0  CPU [████████░░░░░░░░░░░░]  45%  MEM [██████░░░░░░░░░░░░░░]  31%
  gke-worker-1  CPU [███░░░░░░░░░░░░░░░░░░]  17%  MEM [████░░░░░░░░░░░░░░░░]  21%
  ...
```

---

## API Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/keys` | Create an API key |
| `POST` | `/v1/vms` | Create a VM |
| `GET` | `/v1/vms` | List VMs |
| `GET` | `/v1/vms/{name}` | Get VM details |
| `DELETE` | `/v1/vms/{name}` | Delete a VM |
| `PATCH` | `/v1/vms/{name}` | Rename a VM |
| `POST` | `/v1/vms/{name}/restart` | Restart a VM |
| `PATCH` | `/v1/vms/{name}/auth` | Toggle oauth2 auth on/off |
| `GET` | `/v1/vms/{name}/exec` | WebSocket shell session (tmux) |
| `GET` | `/v1/vms/{name}/exec-command?cmd=` | WebSocket exec for arbitrary command (no tmux) |
| `POST` | `/v1/vms/{name}/run` | Run a shell command, return stdout+stderr |
| `POST` | `/v1/vms/{name}/files?path=` | Upload files (tar stream) |
| `GET` | `/v1/vms/{name}/files?path=` | Download files (tar stream) |
| `GET` | `/v1/resources` | Get your resource usage |
| `GET` | `/v1/admin/resources` | Get cluster-wide resource usage (admin only) |

All endpoints except `/healthz` and `/v1/keys` require `X-API-Key` header.

---

## Current Status

| Environment | Status | Setup |
|---|---|---|
| Local (macOS + Docker) | Working | [scripts/local-setup.sh](./scripts/local-setup.sh) |
| GKE Standard (Terraform) | Working | [scripts/terraform-gke/](./scripts/terraform-gke/) |

---

## Contributing

PRs and issues are welcome!
Please open an issue first for large changes.

## License

Apache 2.0 — see [LICENSE](./LICENSE).
