# コンテキスト共有機能 設計ドキュメント

## 背景

InfraBoxの各VMは独立したPVC（8Gi）を持ち、VM間でファイルを共有する仕組みがない。
会社の概要、IR情報、売上データ、顧客情報、提案書などのコンテキストを
マークダウン形式でVM間で共有し、Claude Code等のAIエージェントが
エージェンティックサーチで活用できるようにしたい。

## 要件

- テキストベース（主にMarkdown）のコンテキストファイルをVM間で共有
- AIエージェント（Claude Code）がripgrep/glob等で検索可能
- 現在はGoogleドライブで管理しているが、それに縛られない設計

## 方式比較

### 方式A: 共有PVC（ReadWriteMany）

全VMに共通のPVCを読み取り専用でマウントする。

```
[管理者VM] --書き込み--> [共有PVC: /shared/context/]
[VM-1] --読み取り--> [共有PVC: /shared/context/]
[VM-2] --読み取り--> [共有PVC: /shared/context/]
```

| 項目 | 評価 |
|------|------|
| リアルタイム性 | ◎ 即時反映 |
| 導入コスト | △ RWX対応StorageClass（NFS等）が必要 |
| 運用コスト | ○ マウントするだけ |
| 検索性能 | ◎ ローカルファイルと同等 |
| Googleドライブ連携 | △ 別途syncが必要 |

**必要な変更:**
- NFS Provisioner（nfs-subdir-external-provisioner等）のデプロイ
- 共有PVC（ReadWriteMany）の作成
- VM Deployment specにVolumeMount追加（`/home/ubuntu/context`）

### 方式B: Gitリポジトリ同期

コンテキスト専用のGitリポジトリを用意し、VM起動時にclone、定期的にpullする。

```
[GitHub: company-context repo]
  ├── company/overview.md
  ├── ir/2024-q4.md
  ├── sales/monthly-report.md
  └── customers/client-a/proposal.md

[VM起動時] git clone → /home/ubuntu/context/
[定期sync] cron: git -C /home/ubuntu/context pull (5分間隔)
```

| 項目 | 評価 |
|------|------|
| リアルタイム性 | ○ 数分の遅延（cron間隔） |
| 導入コスト | ◎ 追加インフラ不要 |
| 運用コスト | ◎ git push するだけ |
| 検索性能 | ◎ ローカルファイルと同等 |
| Googleドライブ連携 | ○ GitHub Actions等でGDrive→Git同期可能 |
| バージョン管理 | ◎ Git履歴で変更追跡可能 |

**必要な変更:**
- コンテキスト用Gitリポジトリの作成
- VM initContainerまたは起動スクリプトでgit clone
- cronジョブまたはsystemd timerで定期pull
- Dockerfileにgit認証設定の仕組みを追加

### 方式C: オブジェクトストレージ + ローカル同期

S3/GCS/MinIOにコンテキストファイルを格納し、各VMにrclone等で同期する。

```
[GCS/S3バケット: context-store]
  ├── company/overview.md
  ├── ir/2024-q4.md
  └── ...

[VM] rclone sync remote:context-store /home/ubuntu/context/ (定期実行)
```

| 項目 | 評価 |
|------|------|
| リアルタイム性 | ○ 数分の遅延 |
| 導入コスト | ○ バケット作成 + rclone設定 |
| 運用コスト | ○ バケットにアップロードするだけ |
| 検索性能 | ◎ ローカルファイルと同等 |
| Googleドライブ連携 | ◎ rcloneでGDrive直接同期可能 |
| スケーラビリティ | ◎ 大量ファイルに強い |

**必要な変更:**
- オブジェクトストレージバケットの作成
- rcloneをベースイメージに追加
- サービスアカウント鍵の配布
- cronジョブで定期sync

### 方式D: Googleドライブ直接マウント（rclone mount）

rcloneでGoogleドライブをFUSEマウントし、直接参照する。

| 項目 | 評価 |
|------|------|
| リアルタイム性 | ◎ 即時（ネットワーク経由） |
| 導入コスト | ○ rclone + OAuth設定 |
| 運用コスト | ◎ 既存のGDrive運用をそのまま継続 |
| 検索性能 | △ ネットワーク越しで遅い、キャッシュで改善可能 |
| Googleドライブ連携 | ◎ そのまま使える |

**必要な変更:**
- rclone + FUSE をベースイメージに追加
- Google OAuth認証（サービスアカウント）の設定
- rclone mount をsystemdサービスとして起動
- `--vfs-cache-mode full` で検索性能改善

## Googleドライブ直接マウントの注意点

GDriveのFUSEマウント（rclone mount / google-drive-ocamlfuse）は一見簡単だが、
エージェンティックサーチとの相性に大きな課題がある。

- Claude Codeは `glob → ripgrep → read` のパターンで大量ファイルをスキャンする
- FUSEマウントではファイル1つごとにGoogle Drive APIコールが発生し、数百ファイルの検索に数分かかる
- `--vfs-cache-mode full` でキャッシュすれば改善するが、初回キャッシュ構築が遅い
- Google APIのレート制限（1日あたり10億クエリだが、ユーザー単位では100秒あたり100クエリ）に当たりうる

**結論: マウントではなくローカル同期（rclone sync）を推奨。**
ローカルにファイルがあれば検索は即座に完了する。GDriveを引き続き使いたい場合は、
定期的に `rclone sync` でローカルにコピーする方式（方式C）または
GDrive→Git自動同期（方式B + GitHub Actions）を推奨する。

## 推奨: 方式B（Gitリポジトリ同期）

以下の理由から **方式B** を推奨する。

### 選定理由

1. **追加インフラが不要** — GitHubリポジトリだけで完結。NFS/MinIO等の運用負荷がない
2. **エージェンティックサーチとの相性が最良** — ローカルにファイルがあるため、Claude Codeのripgrep/glob/readがそのまま使える
3. **バージョン管理** — 誰がいつ何を変えたかGit履歴で追跡可能
4. **CLAUDE.md連携** — リポジトリルートにCLAUDE.mdを置くことで、Claude Codeに検索の指針を与えられる
5. **Googleドライブからの移行パス** — GitHub Actionsで定期的にGDriveからGitに同期できる
6. **権限管理** — GitHubのリポジトリ権限やDeploy Keyで制御可能

### 実装概要

```
company-context/              # プライベートGitリポジトリ
├── CLAUDE.md                 # エージェントへの検索ガイド
├── company/
│   ├── overview.md           # 会社概要
│   └── organization.md       # 組織図
├── ir/
│   ├── 2024-q4.md
│   └── 2025-q1.md
├── sales/
│   ├── monthly/
│   │   └── 2025-03.md
│   └── targets.md
├── customers/
│   ├── client-a/
│   │   ├── overview.md       # 顧客概要
│   │   ├── proposal-2025.md  # 提案書
│   │   └── meeting-notes/
│   │       └── 2025-03-15.md
│   └── client-b/
│       └── ...
└── knowledge/
    ├── processes.md          # 業務プロセス
    └── templates/            # テンプレート集
```

### CLAUDE.md の例

```markdown
# Company Context Repository

このリポジトリは社内のコンテキスト情報を格納しています。

## ディレクトリ構成
- `company/` — 会社概要、組織情報
- `ir/` — IR情報、決算データ
- `sales/` — 売上レポート、目標
- `customers/<社名>/` — 顧客別情報（概要、提案書、議事録）
- `knowledge/` — 業務ナレッジ、テンプレート

## 検索のヒント
- 顧客情報を探す場合: `customers/` 配下を参照
- 最新の売上: `sales/monthly/` の最新ファイル
- 提案書: `customers/*/proposal*.md` でglob検索
```

### InfraBoxへの組み込み

#### 1. VMへのコンテキスト自動配置

VM作成時にinitContainerでGitリポジトリをcloneする。

```go
// api/k8s/vm.go の initContainer に追加
{
    Name:  "clone-context",
    Image: cfg.BaseImage,
    Command: []string{"bash", "-c", `
        if [ -n "$CONTEXT_REPO" ]; then
            git clone --depth 1 "$CONTEXT_REPO" /home/ubuntu/context
            chown -R ubuntu:ubuntu /home/ubuntu/context
        fi
    `},
    Env: []corev1.EnvVar{
        {Name: "CONTEXT_REPO", Value: cfg.ContextRepo},
    },
    VolumeMounts: []corev1.VolumeMount{
        {Name: "home", MountPath: "/home/ubuntu"},
    },
}
```

#### 2. 定期同期（cron）

ベースイメージの起動スクリプトにcronジョブを追加。

```bash
# /etc/cron.d/context-sync
*/5 * * * * ubuntu cd /home/ubuntu/context && git pull --ff-only 2>/dev/null
```

#### 3. 環境変数での設定

```bash
# 新しい環境変数
INFRABOX_CONTEXT_REPO=https://github.com/your-org/company-context.git
```

#### 4. CLI拡張（将来）

```bash
# コンテキストの手動同期
ib context sync

# コンテキストリポジトリの設定
ib context set-repo https://github.com/your-org/company-context.git
```

### Googleドライブからの移行

GitHub Actionsで定期同期するワークフローを用意できる。

```yaml
# .github/workflows/sync-gdrive.yml
name: Sync from Google Drive
on:
  schedule:
    - cron: '0 * * * *'  # 毎時
  workflow_dispatch:

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install rclone
        run: curl https://rclone.org/install.sh | sudo bash
      - name: Configure rclone
        run: |
          mkdir -p ~/.config/rclone
          echo "${{ secrets.RCLONE_CONF }}" > ~/.config/rclone/rclone.conf
      - name: Sync from Google Drive
        run: |
          rclone copy gdrive:shared-context/ ./  \
            --include "*.md" --include "*.txt"
      - name: Commit and push
        run: |
          git config user.name "context-sync"
          git config user.email "bot@example.com"
          git add -A
          git diff --staged --quiet || git commit -m "sync: update from Google Drive"
          git push
```

## 段階的な導入ステップ

1. **Phase 1**: コンテキスト用Gitリポジトリを作成し、既存のGDriveコンテンツを移行
2. **Phase 2**: InfraBox VMのinitContainerにgit clone処理を追加
3. **Phase 3**: 定期sync（cron）を設定
4. **Phase 4**（任意）: GDrive→Git自動同期のGitHub Actionsを設定
5. **Phase 5**（任意）: `ib context` CLIコマンドの追加
