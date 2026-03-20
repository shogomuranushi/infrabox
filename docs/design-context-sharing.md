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
OAuth client_id/secretはK8s Secretで注入する。

### 1. ベースイメージ: rcloneのインストールのみ

```dockerfile
# images/base/Dockerfile に追加
RUN curl https://rclone.org/install.sh | bash
```

### 2. Admin: K8s Secretを作成（1回だけ）

GCPでOAuthクライアントを作成し、K8s Secretとして登録する。

```bash
# 1. GCPコンソールでOAuthクライアントID（デスクトップアプリ）を作成
#    - Google Drive APIを有効化
#    - OAuth同意画面を設定

# 2. rclone設定テンプレート（token無し）をK8s Secretとして作成
cat <<'EOF' > /tmp/rclone.conf
[gdrive]
type = drive
scope = drive.readonly
client_id = YOUR_CLIENT_ID.apps.googleusercontent.com
client_secret = YOUR_CLIENT_SECRET
EOF

kubectl create secret generic rclone-config-template \
  --from-file=rclone.conf=/tmp/rclone.conf \
  -n infrabox-system

rm /tmp/rclone.conf
```

このSecretは全VMで共有される。client_id/secretだけではGDriveにアクセスできない
（ユーザーのブラウザ認証が必須）ため、Secret内に持つのは安全。

### 3. VM作成時: Init Containerで注入

既存のInit Container `setup-ssh` にrclone設定のコピー処理を追加する。

```go
// api/k8s/vm.go — createDeployment()

// Init Container のコマンドに追加:
// rclone設定テンプレートのコピー（Secretがマウントされている場合のみ）
if [ -f /run/secrets/rclone-config/rclone.conf ]; then
    mkdir -p /home/ubuntu/.config/rclone
    cp /run/secrets/rclone-config/rclone.conf /home/ubuntu/.config/rclone/rclone.conf
    chown ubuntu:ubuntu /home/ubuntu/.config/rclone/rclone.conf
    chmod 600 /home/ubuntu/.config/rclone/rclone.conf
fi

// VolumeMounts に追加:
{Name: "rclone-config", MountPath: "/run/secrets/rclone-config", ReadOnly: true}

// Volumes に追加:
{
    Name: "rclone-config",
    VolumeSource: corev1.VolumeSource{
        Secret: &corev1.SecretVolumeSource{
            SecretName:  "rclone-config-template",
            DefaultMode: pointer.Int32(0400),
            Optional:    pointer.Bool(true),  // Secretが無くてもVM作成は成功する
        },
    },
}
```

`Optional: true` により、rclone設定が不要な環境ではSecretを作らなくてもよい。

### ユーザー: 初回セットアップ（ブラウザ認証のみ）

client_id/secretは設定済みなので、ユーザーはGoogle認証するだけ。

```bash
# VMにSSH
ib ssh my-vm

# rclone設定を確認（client_id/secretは入っている）
cat ~/.config/rclone/rclone.conf

# 対話式で認証（ブラウザでGoogleログイン）
rclone config reconnect gdrive:
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
