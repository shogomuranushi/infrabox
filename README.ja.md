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
$ ib create my-app
完了（7秒）

  Shell: ib ssh my-app
  URL:   https://my-app.infra.example.com
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
| 🔌 | WebSocket 経由のシェルアクセス（SSH 鍵管理不要） |
| 📂 | ファイル転送（API 経由のアップロード / ダウンロード） |
| 🌐 | HTTPS URL 自動払い出し |
| 🔒 | Private / Public / External 共有設定 |
| 🔐 | Google Workspace & Entra ID SSO |
| 🎟️ | 招待コードによるオープンモード登録 |
| 🛡️ | ユーザーごとの Namespace 分離 & ResourceQuota |
| 💾 | 永続ディスク（GCE PD / PVC） |
| 📁 | rclone による Google Drive コンテキスト共有 |
| 🔑 | VM ごとの oauth2 認証トグル（エンドポイント単位で有効/無効） |
| 📊 | リソース使用量の可視化（`ib top` / `ib admin top`） |
| 📦 | `ib` CLI ツール |

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
│      Kubernetes Cluster（k3s または GKE Standard）      │
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

### シェルアクセスの仕組み

InfraBox は SSH の代わりに **WebSocket + K8s exec** を使用します:

```
ib ssh myvm
  → WebSocket (wss://api.example.com/v1/vms/myvm/exec)
  → API サーバーが API キーで認証
  → K8s pod exec (SPDY) で VM コンテナに接続
  → インタラクティブな bash セッション
```

これにより:
- **SSH 鍵の管理が不要** — API キーだけで OK
- **SSH ポート (2222) が不要** — すべての通信は HTTPS (443) 経由
- **sshpiper や SSH プロキシが不要** — コンポーネントが少なくシンプル

### OSS Stack

| コンポーネント | OSS | 役割 |
|---|---|---|
| HTTPS Proxy | [ingress-nginx](https://github.com/kubernetes/ingress-nginx) + [cert-manager](https://github.com/cert-manager/cert-manager) | TLS 終端、ワイルドカード証明書 |
| SSO | [oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) | Google Workspace / Entra ID 認証 |
| VM 管理 | InfraBox API (Go) | VM CRUD、exec、ファイル転送、クォータ |

---

## Getting Started

### ユーザー向け

#### 1. ib CLI をインストール

```bash
curl -fsSL https://github.com/shogomuranushi/infrabox/releases/latest/download/install.sh | sudo sh
```

バイナリを直接ダウンロードする場合は [Releases](https://github.com/shogomuranushi/infrabox/releases) から。

#### 2. セットアップ（管理者から招待コードを受け取ってから実行）

```bash
ib init
```

```
Endpoint [https://api.infrabox.example.com]:
Name (e.g. your email): you@example.com
Invitation code: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx

✓ Setup complete. Run 'ib create <name>' to create a VM.
```

#### 3. 最初のVMを作る

```bash
ib create my-app
```

```
Ready (7s)

  Shell:     ib ssh my-app
  HTTPS URL: https://my-app.infra.example.com
```

#### CLI コマンド一覧

```bash
ib create my-app           # VM を作成
ib list                    # VM 一覧
ib ssh my-app              # VM でシェルを開く
ib scp ./file myvm:/tmp/   # ファイルを VM にアップロード
ib scp myvm:/tmp/f ./      # ファイルを VM からダウンロード
ib rename old new          # VM をリネーム
ib delete my-app           # VM を削除
ib auth enable my-app      # VM の HTTPS エンドポイントに oauth2 認証を有効化
ib auth disable my-app     # 認証を無効化（完全オープン）
ib top                     # 自分のリソース使用量を表示（CPU / メモリ / VM 数）
ib upgrade                 # CLI を最新版に更新
```

---

### 管理者向け

#### 1. サーバーのデプロイ

**オプション A — GCE + k3s（Terraform）**

```bash
cd scripts/terraform-gce
cp terraform.tfvars.example terraform.tfvars  # 値を設定
terraform init
terraform apply
```

必須変数: `gcp_project`, `domain`, `letsencrypt_email`。
詳細オプションは [scripts/terraform-gce/](./scripts/terraform-gce/) を参照。

**オプション B — GKE Standard（Terraform）**

```bash
cd scripts/terraform-gke
cp terraform.tfvars.example terraform.tfvars  # 値を設定
terraform init
terraform apply
```

必須変数: `project_id`, `domain`, `letsencrypt_email`。
詳細オプションは [scripts/terraform-gke/](./scripts/terraform-gke/) を参照。

#### 2. 管理者APIキーを保存

```bash
ib admin init
# Admin API key: <terraform output admin_api_key の値>
```

#### 3. ユーザーへの招待コードを発行

```bash
# 1回限りの招待コードを作成
ib admin invite create

# 発行済みコード一覧
ib admin invite list
```

発行したコードをユーザーに共有し、`ib init` 実行時に入力してもらいます。

#### 4. クラスターのリソース状況を確認

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

## API エンドポイント

| メソッド | パス | 説明 |
|---|---|---|
| `POST` | `/v1/keys` | API キー作成 |
| `POST` | `/v1/vms` | VM 作成 |
| `GET` | `/v1/vms` | VM 一覧 |
| `GET` | `/v1/vms/{name}` | VM 詳細 |
| `DELETE` | `/v1/vms/{name}` | VM 削除 |
| `PATCH` | `/v1/vms/{name}` | VM リネーム |
| `POST` | `/v1/vms/{name}/restart` | VM 再起動 |
| `PATCH` | `/v1/vms/{name}/auth` | oauth2 認証の有効/無効切り替え |
| `GET` | `/v1/vms/{name}/exec` | WebSocket シェルセッション |
| `POST` | `/v1/vms/{name}/files?path=` | ファイルアップロード（tar ストリーム） |
| `GET` | `/v1/vms/{name}/files?path=` | ファイルダウンロード（tar ストリーム） |
| `GET` | `/v1/resources` | 自分のリソース使用量を取得 |
| `GET` | `/v1/admin/resources` | クラスター全体のリソース使用量を取得（管理者のみ） |

`/healthz` と `/v1/keys` 以外のエンドポイントは `X-API-Key` ヘッダが必要です。

---

## 現在の実装状況

| 環境 | 状態 | セットアップ |
|---|---|---|
| ローカル（macOS + Docker） | 動作確認済み | [scripts/local-setup.sh](./scripts/local-setup.sh) |
| GCE / VPS（k3s） | 動作確認済み | [scripts/gce-setup.sh](./scripts/gce-setup.sh) |
| GCE（Terraform + k3s） | 動作確認済み | [scripts/terraform-gce/](./scripts/terraform-gce/) |
| GKE Standard（Terraform） | 動作確認済み | [scripts/terraform-gke/](./scripts/terraform-gke/) |

---

## Contributing

PR・Issue歓迎です。
大きな変更の場合は、まずIssueで相談してください。

## ライセンス

Apache 2.0 — [LICENSE](./LICENSE) を参照。
