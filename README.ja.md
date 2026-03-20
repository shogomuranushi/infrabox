# InfraBox

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23+-00ADD8.svg)](https://go.dev/)

**Linuxマシンが10秒で手に入る。**

エンジニア向けセルフホスト型VM基盤。
[exe.dev](https://exe.dev) の哲学に深いリスペクトを込めて。

[English README](./README.md)

---

## InfraBox とは

エンジニアひとりひとりに、秒速でLinuxマシンを届けるツールです。

```bash
$ ib new my-app
✓ 完了（7秒）

  SSH:  ssh my-app.infra.example.com
  URL:  https://my-app.infra.example.com  (private)
```

- **Webアプリとしてすぐ公開** — 作成と同時にHTTPS URLが払い出され、即座に外部公開可能
- **常時稼働** — cronジョブ・Slack bot・バックグラウンドサービスの実行基盤として最適
- **AIエージェント対応** — Claude Code / Codex をプリインストール済み。すぐに開発を始められる
- **セキュアな分離** — VM同士は完全に隔離されており、実験が他環境に影響しない
- **リソース効率** — 従来のVMと違い固定リソースを専有しない。アイドル時は最小限のリソースしか使わず、使うときだけ自動で拡張するため、同じインフラにより多くの環境を効率よく収容できる

エンタープライズ向け開発環境管理プラットフォームとは設計思想が異なります。
Terraform 不要。DB 管理不要。Kubernetes と最小限の OSS だけで動きます。

---

## Features

| | Feature |
|---|---|
| 🖥️ | VM 作成 / 一覧 / 削除 / 再起動 / リネーム |
| 🔑 | SSH アクセス |
| 🌐 | HTTPS URL 自動払い出し |
| 🔒 | Private / Public / External 共有設定 |
| 🔐 | Google Workspace & Entra ID SSO |
| 🎟️ | 招待コードによるオープンモード登録 |
| 🛡️ | ユーザーごとの Namespace 分離 & ResourceQuota |
| 💾 | 永続ディスク（GCE PD / PVC） |
| 📁 | rclone による Google Drive コンテキスト共有 |
| 📦 | `ib` CLI ツール |

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
┌────────────────────────────────────────────────────────┐
│              Kubernetes Cluster (k3s)                  │
│                                                        │
│  API Node (on-demand)                                  │
│  ┌──────────────────────────────────────────────────┐  │
│  │  sshpiper ─── ContainerSSH ──▶ InfraBox API     │  │
│  │  nginx-ingress + cert-manager    Dex (OIDC)      │  │
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

### 前提条件

- InfraBox サーバーが起動していること
  → 自分でセットアップする場合は [scripts/](./scripts/) を参照

### ib CLI のインストール

```bash
curl -fsSL https://github.com/shogomuranushi/infrabox/releases/latest/download/install.sh | sh
```

バイナリを直接ダウンロードする場合は [Releases](https://github.com/shogomuranushi/infrabox/releases) から。

### セットアップ

```bash
ib init   # APIキーを入力 → SSHキーが ~/.ib/id_infrabox に自動生成される
```

### 最初のVMを作る

```bash
ib new my-app
```

```
✓ Ready (7s)

  SSH:       ib ssh my-app
  HTTPS URL: https://my-app.infra.example.com
```

```bash
ib ssh my-app        # VMにSSH接続
ib list              # VM一覧
ib rename old new    # VMをリネーム
ib delete my-app     # VMを削除
ib upgrade           # CLIを最新版に更新
```

---

## 現在の実装状況

| 環境 | 状態 | セットアップ |
|---|---|---|
| ローカル（macOS + Docker） | ✅ 動作確認済み | [scripts/local-setup.sh](./scripts/local-setup.sh) |
| GCE / VPS（k3s） | ✅ 動作確認済み | [scripts/gce-setup.sh](./scripts/gce-setup.sh) |
| GCE（Terraform） | ✅ 動作確認済み | [scripts/terraform-gce/](./scripts/terraform-gce/) |
| GKE / EKS などマネージド K8s | 🚧 対応予定 | — |

---

## Contributing

PR・Issue歓迎です。
大きな変更の場合は、まずIssueで相談してください。

## ライセンス

Apache 2.0 — [LICENSE](./LICENSE) を参照。
