# コンテキスト共有機能 設計ドキュメント

## 背景

会社の概要、IR情報、売上データ、顧客情報、提案書などのコンテキストを
VM上のAIエージェント（Claude Code等）がエージェンティックサーチで活用できるようにしたい。
現在はGoogleドライブでテキストベース（Markdown等）で管理している。

## 要件

- Googleドライブの権限を継承（ユーザーが参照可能なファイルのみアクセス可能）
- 常にローカルに同期された状態（検索が高速であること）
- オープンソースで実現

## 方針

**rclone（OSS、Go製）でGoogleドライブをローカルに同期する。**
ユーザーが各自のVMで `rclone config` → `rclone sync` するだけ。
InfraBox側の変更はベースイメージへのrcloneプリインストールのみ。

### なぜrcloneか

- 最も成熟したOSSクラウドストレージ同期ツール（40以上のプロバイダ対応）
- ユーザー自身のOAuth認証で動くため、**GDriveの権限がそのまま継承される**
- `rclone sync` でローカルコピーを保持 → ripgrep/globで高速検索
- FUSEマウント（`rclone mount`）は検索が遅いため非推奨

### なぜマウントではなくsyncか

| | rclone mount | rclone sync |
|---|---|---|
| 権限継承 | ◎ | ◎ |
| 検索速度 | △ 初回遅い、API制限リスク | ◎ 常にローカル |
| 安定性 | △ NW断でハング | ◎ 失敗しても次回リトライ |
| リアルタイム性 | ◎ 即時 | ○ cron間隔の遅延 |

## InfraBox側の変更

ベースイメージ（GHCR公開）にはABEJA固有の情報を含めない。
OAuth client_id/secretは環境変数でVM Podに注入する。

rcloneは `RCLONE_DRIVE_CLIENT_ID` / `RCLONE_DRIVE_CLIENT_SECRET` 環境変数を認識するため、
ファイルコピーやInit Container変更が不要でシンプル。

### 1. ベースイメージ: rcloneのインストールのみ

```dockerfile
# images/base/Dockerfile に追加
RUN curl https://rclone.org/install.sh | bash
```

### 2. Admin: APIの環境変数にOAuth情報を設定（1回だけ）

GCPでOAuthクライアントを作成し、API Deploymentの環境変数に設定する。

```bash
# 1. GCPコンソールでOAuthクライアントID（デスクトップアプリ）を作成
#    - Google Drive APIを有効化
#    - OAuth同意画面を設定

# 2. K8s Secretに格納し、API Deploymentから参照
kubectl create secret generic rclone-oauth \
  --from-literal=client-id='YOUR_CLIENT_ID.apps.googleusercontent.com' \
  --from-literal=client-secret='YOUR_CLIENT_SECRET' \
  -n infrabox-system
```

### 3. コード変更

**api/config/config.go** — 設定を追加:

```go
type Config struct {
    // ... 既存フィールド ...
    RcloneDriveClientID     string
    RcloneDriveClientSecret string
}

func Load() *Config {
    return &Config{
        // ... 既存 ...
        RcloneDriveClientID:     getEnv("INFRABOX_RCLONE_DRIVE_CLIENT_ID", ""),
        RcloneDriveClientSecret: getEnv("INFRABOX_RCLONE_DRIVE_CLIENT_SECRET", ""),
    }
}
```

**api/k8s/vm.go** — VMConfigに追加し、Pod envに渡す:

```go
type VMConfig struct {
    // ... 既存フィールド ...
    RcloneDriveClientID     string
    RcloneDriveClientSecret string
}
```

Containerのenvに追加（値が空なら追加しない）:

```go
// createDeployment() 内で env を組み立て
var env []corev1.EnvVar
if cfg.RcloneDriveClientID != "" {
    env = append(env, corev1.EnvVar{
        Name: "RCLONE_DRIVE_CLIENT_ID", Value: cfg.RcloneDriveClientID,
    })
    env = append(env, corev1.EnvVar{
        Name: "RCLONE_DRIVE_CLIENT_SECRET", Value: cfg.RcloneDriveClientSecret,
    })
}

// Container spec
Env: env,  // rclone未設定なら空 = 既存動作に影響なし
```

**k8s/api-deployment.yaml** — SecretからAPIに環境変数を渡す:

```yaml
env:
  - name: INFRABOX_RCLONE_DRIVE_CLIENT_ID
    valueFrom:
      secretKeyRef:
        name: rclone-oauth
        key: client-id
        optional: true
  - name: INFRABOX_RCLONE_DRIVE_CLIENT_SECRET
    valueFrom:
      secretKeyRef:
        name: rclone-oauth
        key: client-secret
        optional: true
```

### ユーザー: 初回セットアップ（ブラウザ認証のみ）

環境変数でclient_id/secretは設定済み。ユーザーはrclone設定とGoogle認証するだけ。

```bash
# VMにSSH
ib ssh my-vm

# rclone設定（client_id/secretは環境変数にあるので入力不要）
rclone config
# name> gdrive
# Storage> drive
# client_id> （空のままEnter — 環境変数が使われる）
# client_secret> （空のままEnter — 環境変数が使われる）
# scope> 1  (drive.readonly)
# → "Use web browser to automatically authenticate?" に No
# → 表示されたURLをローカルブラウザで開く → Googleログイン → 許可
# → 認証コードをVMに貼り付け → 完了

# 初回同期
mkdir -p ~/context
rclone sync gdrive:"共有フォルダ" ~/context/ \
  --include "*.md" --include "*.txt" --include "*.csv"
```

### ユーザー: 定期同期の設定

```bash
# crontabで5分間隔の自動同期
(crontab -l 2>/dev/null; echo '*/5 * * * * rclone sync gdrive:"共有フォルダ" ~/context/ --include "*.md" --include "*.txt" --include "*.csv" --log-file=/tmp/rclone-sync.log') | crontab -
```

### Claude Codeでの活用

同期されたファイルはローカルにあるので、Claude Codeがそのまま検索できる。

```bash
# Claude Codeに聞く例
claude "~/context/ にある顧客Aの提案書を要約して"
claude "~/context/ から最新のIR情報を探して売上推移をまとめて"
```

`~/context/CLAUDE.md` を置いておくと、検索の指針を与えられる。

```markdown
# Company Context (Google Drive synced)

このディレクトリにはGoogleドライブから同期されたコンテキストファイルがあります。

## ディレクトリ構成
- 会社概要/ — 会社紹介、組織図
- IR/ — 決算データ
- 営業/ — 売上レポート、顧客別情報
- ナレッジ/ — 業務フロー、テンプレート
```

## セキュリティ

- 各ユーザーが自分のGoogleアカウントでOAuth認証 → 自分が見えるファイルだけ同期
- rclone設定（トークン含む）は `~/.config/rclone/rclone.conf` に保存（VMのPVC上）
- スコープは `drive.readonly` に限定
- VM削除時にPVCも削除 → 同期データ・トークンも消える
- ユーザーがGoogleアカウント側でアクセスをrevokeすれば即座に同期停止
