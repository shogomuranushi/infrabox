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
$ ib new my-app
✓ Ready (7s)

  SSH:  ssh my-app.infra.example.com
  URL:  https://my-app.infra.example.com  (private)
```

- **Instant web publishing** — every VM gets a public HTTPS URL out of the box
- **Always-on** — perfect for cron jobs, Slack bots, and background services
- **AI-ready** — Claude Code and Codex are pre-installed; start coding with an agent immediately
- **Secure by default** — each VM is fully isolated; experiments never affect each other
- **Resource-efficient** — unlike traditional VMs that lock in fixed resources, containers stay minimal when idle and scale up automatically when in use — fitting far more environments on the same infrastructure

Unlike enterprise-grade dev environment platforms, InfraBox is intentionally thin.
No Terraform. No databases to manage. Just Kubernetes + a handful of OSS components.

---

## Features

| | Feature |
|---|---|
| 🖥️ | VM create / list / delete / restart |
| 🔑 | SSH access |
| 🌐 | HTTPS URL auto-provisioning |
| 🔒 | Private / Public / External sharing |
| 🔐 | Google Workspace & Entra ID SSO |
| 💾 | Persistent disk (PVC) |
| 📦 | `ib` CLI tool |

---

## Architecture

```
┌──────────────────────────────────────────────────────┐
│                        User                          │
│   ssh my-app.infra.example.com                       │
│   https://my-app.infra.example.com                   │
└───────────────┬──────────────────────┬───────────────┘
                │ SSH:22               │ HTTPS:443
                ▼                      ▼
 ┌──────────────────────┐   ┌──────────────────────────┐
 │      sshpiper        │   │  nginx-ingress +         │
 │   (SSH reverse proxy)│   │  cert-manager            │
 └───────────┬──────────┘   └────────────┬─────────────┘
             │                           │
             ▼                           ▼
┌────────────────────────────────────────────────────────┐
│              Kubernetes Cluster                        │
│                                                        │
│  ┌─────────────────┐       ┌────────────────────────┐ │
│  │  ContainerSSH   │──────▶│   InfraBox API (Go)    │ │
│  └────────┬────────┘       └────────────────────────┘ │
│           ▼                                            │
│  ┌─────────────────────────────────────────────────┐  │
│  │  VM Pods                                        │  │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐        │  │
│  │  │ my-app   │ │ demo-env │ │agent-01  │  ...   │  │
│  │  │ Ubuntu   │ │ Ubuntu   │ │ Ubuntu   │        │  │
│  │  │ PVC:20GB │ │ PVC:20GB │ │ PVC:20GB │        │  │
│  │  └──────────┘ └──────────┘ └──────────┘        │  │
│  └─────────────────────────────────────────────────┘  │
│                                                        │
│  ┌──────────┐  ┌──────────┐                           │
│  │   Dex    │  │ sshpiper │                           │
│  │  (OIDC)  │  │  Pipes   │                           │
│  └──────────┘  └──────────┘                           │
└────────────────────────────────────────────────────────┘
```

### OSS Stack

| Component | OSS | Role |
|---|---|---|
| SSH Proxy | [sshpiper](https://github.com/tg123/sshpiper) | Route SSH by VM name |
| SSH → Pod | [ContainerSSH](https://github.com/ContainerSSH/ContainerSSH) | Spawn/connect to K8s Pod per SSH session |
| HTTPS Proxy | [ingress-nginx](https://github.com/kubernetes/ingress-nginx) + [cert-manager](https://github.com/cert-manager/cert-manager) | TLS termination, wildcard cert |
| SSO | [Dex](https://github.com/dexidp/dex) | OIDC broker for Google Workspace / Entra ID |
| VM Management | InfraBox API (Go) | VM CRUD, quota, access control |

---

## Getting Started

### Prerequisites

- An InfraBox server is up and running
  → See [scripts/](./scripts/) to set up your own

### Install ib CLI

```bash
curl -fsSL https://github.com/shogomuranushi/infrabox/releases/latest/download/install.sh | sh
```

Or download the binary directly from [Releases](https://github.com/shogomuranushi/infrabox/releases).

### Set up

```bash
ib init   # Enter your API key → SSH key is auto-generated at ~/.ib/id_infrabox
```

### Create your first VM

```bash
ib new my-app
```

```
✓ Ready (7s)

  SSH:       ib ssh my-app
  HTTPS URL: https://my-app.infra.example.com
```

```bash
ib ssh my-app    # SSH into the VM
ib list          # List your VMs
ib delete my-app # Delete a VM
ib upgrade       # Upgrade the CLI to the latest version
```

---

## Current Status

| Environment | Status | Setup |
|---|---|---|
| Local (macOS + Docker) | ✅ Working | [scripts/local-setup.sh](./scripts/local-setup.sh) |
| GCE / VPS (k3s) | ✅ Working | [scripts/gce-setup.sh](./scripts/gce-setup.sh) |
| GKE / EKS / other managed K8s | 🚧 Coming soon | — |

---

## Contributing

PRs and issues are welcome!
Please open an issue first for large changes.

## License

Apache 2.0 — see [LICENSE](./LICENSE).
