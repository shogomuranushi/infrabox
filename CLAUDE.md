# InfraBox — Agent Guide

## Repository Structure

```
infrabox/
├── api/          # VM management API (Go / Gin)
├── cli/          # `ib` CLI tool (Go / Cobra)
├── images/base/  # Base VM image (Ubuntu 24.04 Dockerfile)
├── k8s/          # Kubernetes manifests
├── scripts/      # Setup / teardown scripts
└── .github/workflows/
    ├── build-base-image.yml  # Triggered on images/base/** changes
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

## Module Path

CLI module: `github.com/shogomuranushi/infrabox/cli`
