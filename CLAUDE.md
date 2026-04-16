# InfraBox — Agent Guide

## Repository Structure

```
infrabox/
├── api/          # VM management API (Go / Chi)
├── cli/          # `ib` CLI tool (Go / Cobra)
├── images/base/  # Base VM image (Ubuntu 24.04 Dockerfile)
├── scripts/      # Setup / teardown scripts
└── .github/workflows/
    ├── build-base-image.yml  # Triggered on images/base/** changes
    ├── deploy-api.yml        # Triggered on api/** or k8s/** changes; also patches nginx ingress
    └── release-cli.yml       # Triggered on v* tags
```

## CLI Release Procedure

To publish a new `ib` CLI release to GitHub Releases:

```bash
git tag v1.2.3
git push origin v1.2.3
```

This triggers `.github/workflows/release-cli.yml`, which runs goreleaser and publishes binaries for:
- linux/amd64, linux/arm64
- darwin/amd64, darwin/arm64
- windows/amd64

Release assets are defined in `.goreleaser.yml`.

## Base Image Release

`ghcr.io/shogomuranushi/infrabox-base:ubuntu-24.04` is rebuilt automatically when
`images/base/**` is pushed to `main` (see `build-base-image.yml`).

## API Deploy Procedure

Merging to `main` with changes under `api/**` triggers `deploy-api.yml`, which:
1. Builds and pushes a new Docker image to GHCR
2. Patches all nginx ingresses in the deploy namespace to allow 200m body size
3. Runs `kubectl rollout restart deployment/infrabox-api`

To trigger manually: `gh workflow run deploy-api.yml --ref main`

## Key API Endpoints (internal)

- `GET  /v1/vms/{name}/exec` — WebSocket tmux shell session
- `GET  /v1/vms/{name}/exec-command?cmd=` — WebSocket direct command (no tmux); used by `ib ssh-proxy` for long-running processes
- `POST /v1/vms/{name}/run` — one-shot command execution, returns stdout+stderr; used by `ib ssh-proxy` for env detection and short commands
- `POST /v1/vms/{name}/files?path=` — tar upload (nginx body limit: 200m)

## Claude Code SSH Integration

`ib ssh-proxy` implements an SSH server on stdin/stdout and bridges to the WebSocket exec endpoint. It handles:
- `shell` requests → tmux session via `/exec`
- `exec` requests → one-shot via `/run`; long-running (`--connect`) via `/exec-command`
- `sftp` subsystem → in-memory buffering + upload via `/files`

Users add to `~/.ssh/config`:
```
Host infrabox-*
  User ubuntu
  ProxyCommand ib ssh-proxy %h
```

## Module Path

CLI module: `github.com/shogomuranushi/infrabox/cli`
