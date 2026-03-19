# Implementation: Per-User Namespace Isolation with ResourceQuota

## Final Spec

| Item | Value | Configurable |
|------|-------|-------------|
| ResourceQuota | `requests.cpu=2, requests.memory=8Gi` | `INFRABOX_USER_CPU_QUOTA`, `INFRABOX_USER_MEMORY_QUOTA` |
| VM requests | 200m / 800Mi | - |
| VM limits | 1CPU / 2Gi | - |
| PVC per VM | 8Gi | - |
| MaxVMsPerUser | 10 | `INFRABOX_MAX_VMS_PER_USER` |
| User Namespace | `infrabox-vms-<username>` | base from `INFRABOX_VM_NAMESPACE` |

## Changes Made

### api/config/config.go
- Added `UserCPUQuota` and `UserMemoryQuota` config fields
- Changed MaxVMsPerUser default from 15 to 10

### api/k8s/vm.go
- Added `Owner` field to `VMConfig`
- Added `UserNamespace()` helper to compute per-user namespace name
- Added `EnsureUserNamespace()` to create namespace + ResourceQuota
- Updated `vmLabels()` to include `infrabox-owner` label
- Changed VM resource requests to 200m/800Mi, limits to 1CPU/2Gi
- Changed PVC size from 20Gi to 8Gi

### api/handlers/vms.go
- `CreateVM`: computes per-user namespace, calls `EnsureUserNamespace`, stores namespace in DB
- `DeleteVM`: reads namespace from DB (falls back to default for pre-migration VMs)
- `RestartVM`: reads namespace from DB

### api/db/db.go
- Added `Namespace` field to `VM` struct
- Added `namespace` column to schema (with ALTER TABLE migration for existing DBs)
- Updated all queries to include namespace

### k8s/rbac.yaml
- Added `namespaces` and `resourcequotas` permissions to ClusterRole
