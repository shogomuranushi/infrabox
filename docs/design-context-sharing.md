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

ベースイメージにrcloneをプリインストールするだけ。

```dockerfile
# images/base/Dockerfile に追加
RUN curl https://rclone.org/install.sh | bash
```

## ユーザー側のセットアップ手順

### 前提: Admin（1回だけ）

GCPコンソールでOAuthクライアントを1つ作成し、チームに共有する。

1. GCPプロジェクトでGoogle Drive APIを有効化
2. OAuth同意画面を設定
3. OAuthクライアントID（デスクトップアプリ）を作成
4. Client ID / Client Secret をチームに共有

### ユーザー: 初回セットアップ

```bash
# VMにSSH
ib ssh my-vm

# rclone設定（対話式）
rclone config
# name> gdrive
# Storage> drive
# client_id> （Adminから共有されたID）
# client_secret> （Adminから共有されたSecret）
# scope> 1  (drive.readonly)
# → ブラウザが開く → Googleログイン → 許可

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
