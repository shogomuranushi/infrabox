# コンテキスト共有機能 設計ドキュメント

## 背景

InfraBoxの各VMは独立したPVC（8Gi）を持ち、VM間でファイルを共有する仕組みがない。
会社の概要、IR情報、売上データ、顧客情報、提案書などのコンテキストを
マークダウン形式でVM間で共有し、Claude Code等のAIエージェントが
エージェンティックサーチで活用できるようにしたい。

## 要件

- テキストベース（主にMarkdown）のコンテキストファイルをVM間で共有
- AIエージェント（Claude Code）がripgrep/glob等で検索可能
- **Googleドライブの権限を継承** — ユーザーが参照可能なファイルのみアクセスできること
- **常にローカルに同期された状態** — 検索が高速であること
- 現在Googleドライブで管理しているデータをそのまま活用したい
- オープンソースで実現したい

## 方式比較（権限継承要件を踏まえた再評価）

権限継承が必須のため、Gitリポジトリ方式（全員が同じファイルを見る）は不適切。
ユーザーごとのGDrive認証情報でアクセスし、その人が見えるファイルだけがVMに存在する方式が必要。

### 方式E: rclone sync（ローカル同期）+ ユーザー認証 ★推奨

ユーザーのGDrive認証情報で `rclone sync` を定期実行し、ローカルにコピーを保持する。

```
[Googleドライブ]
  ├── 会社概要.md        (全員に共有)
  ├── IR/2025-Q1.md      (全員に共有)
  ├── 顧客A/提案書.md    (営業チームのみ共有)
  └── 経営会議/議事録.md  (経営陣のみ共有)

[VM内: /home/ubuntu/context/]  ← rclone sync で同期
  ├── 会社概要.md        ✅ (このユーザーは閲覧可能)
  ├── IR/2025-Q1.md      ✅
  ├── 顧客A/提案書.md    ✅ (営業チームなので見える)
  └── 経営会議/議事録.md  ❌ (権限なし → 同期されない)
```

| 項目 | 評価 |
|------|------|
| 権限継承 | ◎ GDriveの共有設定がそのまま反映される |
| リアルタイム性 | ○ cron間隔（5分〜）の遅延 |
| 検索性能 | ◎ ローカルファイルなのでripgrep即座に完了 |
| 導入コスト | ◎ rclone（OSS）のみ |
| 運用コスト | ◎ 既存GDrive運用をそのまま継続 |
| 双方向同期 | △ rclone bisyncで可能だが慎重な運用が必要 |

### 方式F: rclone mount（FUSEマウント）+ VFSキャッシュ

ユーザーのGDrive認証情報で `rclone mount --vfs-cache-mode full` を実行。

```
[Googleドライブ] ←FUSE→ [VM内: /home/ubuntu/gdrive/]
                          ├── ローカルキャッシュ: /tmp/rclone-cache/
                          └── アクセス時にキャッシュに取得
```

| 項目 | 評価 |
|------|------|
| 権限継承 | ◎ GDriveの共有設定がそのまま反映される |
| リアルタイム性 | ◎ GDriveの変更が即時に見える |
| 検索性能 | △→○ 初回は遅いがキャッシュ後は高速 |
| 導入コスト | ○ rclone + fuse3 |
| 運用コスト | ◎ 既存GDrive運用をそのまま継続 |
| 双方向同期 | ◎ ネイティブに読み書き可能 |

**FUSEマウントの検索性能の問題:**
- Claude Codeは `glob → ripgrep → read` のパターンで大量ファイルをスキャンする
- 初回アクセス時、ファイルごとにGoogle Drive APIコールが発生
- `--vfs-cache-mode full` でキャッシュすれば2回目以降は高速
- ただし初回キャッシュ構築（数百ファイル）に数分かかる
- Google APIのレート制限（ユーザー単位で100秒あたり100クエリ）に当たりうる

### 方式G: ハイブリッド（rclone sync + mount）

マウント（リアルタイム参照）と同期（高速検索）を組み合わせる。

```
[Googleドライブ]
  ├── FUSE mount → /home/ubuntu/gdrive/       (リアルタイム参照・書き込み用)
  └── rclone sync → /home/ubuntu/context/     (検索用ローカルコピー)
```

| 項目 | 評価 |
|------|------|
| 権限継承 | ◎ |
| リアルタイム性 | ◎ mount経由で即時 |
| 検索性能 | ◎ sync先のローカルコピーを検索 |
| 導入コスト | ○ やや複雑 |
| ディスク使用 | △ 二重にストレージ消費 |

## 推奨: 方式E（rclone sync + ユーザー認証）

### 選定理由

1. **権限継承がシンプル** — ユーザーのOAuth認証でrcloneを動かすだけで、そのユーザーが見えるファイルだけが同期される
2. **検索性能が最良** — 完全にローカルなファイルなのでripgrep/globが最速で動作
3. **安定性** — FUSEマウントはネットワーク断やAPI制限でハングするリスクがあるが、syncは失敗しても次回リトライするだけ
4. **rcloneは成熟したOSS** — Go製、40以上のクラウドストレージに対応、活発にメンテナンス中
5. **ディスク消費が予測可能** — syncされたファイルのサイズ分だけ使用

### オープンソースツール

| ツール | 言語 | 特徴 | 状態 |
|--------|------|------|------|
| **rclone** | Go | 40以上のクラウドストレージ対応、sync/mount/bisync | ◎ 活発（2026年時点） |
| google-drive-ocamlfuse | OCaml | GDrive専用FUSEマウント | ○ メンテ継続 |
| gdsync-linux | - | GDrive同期CLI | △ 新しいプロジェクト |
| FreeFileSync | C++ | GUI同期ツール、GDrive対応 | ○ v14.8（2026） |
| Celeste | - | GUI同期ツール | ✕ 2025年11月に開発終了 |

**rclone一択**。最も成熟しており、sync/mount両対応、OAuthトークンベースの認証に対応。

## 実装設計

### 認証方式: ユーザー個別OAuth（推奨）

各ユーザーが自分のGoogleアカウントでOAuth認証し、そのトークンでrcloneを動かす。
これにより、**そのユーザーがGDrive上で閲覧権限を持つファイルだけ**が同期される。

#### なぜドメイン全体委任ではなく個別OAuthか

| 観点 | ドメイン全体委任 | 個別OAuth |
|------|-----------------|-----------|
| セキュリティ | △ SA鍵漏洩で全員のGDriveが見える | ◎ 自分のトークンのみ |
| 最小権限原則 | ✕ 全ユーザーを代理可能 | ◎ 本人のファイルだけ |
| ユーザー同意 | なし（勝手に代理） | あり（明示的に認可） |
| 対象 | Google Workspace限定 | Gmail個人アカウントも可 |
| Admin負荷 | 重い（Workspace管理者設定） | 軽い（OAuthクライアント作成のみ） |

#### Adminの準備（1回だけ）

GCPコンソールでOAuthクライアントを1つ作成する。

1. GCPプロジェクトでGoogle Drive APIを有効化
2. OAuth同意画面を設定（内部 or 外部）
3. OAuthクライアントID（デスクトップアプリ）を作成
4. Client ID / Client Secret をInfraBoxの環境変数に設定

```bash
INFRABOX_GDRIVE_CLIENT_ID=xxxx.apps.googleusercontent.com
INFRABOX_GDRIVE_CLIENT_SECRET=GOCSPX-xxxx
```

#### ユーザーの認証フロー（1回だけ）

```
$ ib context auth
→ ブラウザが開く
→ Googleアカウントでログイン
→ 「InfraBoxがGoogleドライブの読み取りを要求しています」→ 許可
→ OAuthトークンがInfraBox APIに保存される（K8s Secret経由でVMに配布）

以降は自動:
  [VM起動] → 保存済みトークンでrclone sync → ローカルにファイル同期
  [5分ごと] → rclone sync で差分更新
  [トークン期限切れ] → refresh_tokenで自動更新（ユーザー操作不要）
```

#### rclone設定（自動生成）

```ini
# /etc/rclone/rclone.conf （K8s Secretからマウント、ユーザーごとに生成）
[gdrive]
type = drive
scope = drive.readonly
client_id = ${INFRABOX_GDRIVE_CLIENT_ID}
client_secret = ${INFRABOX_GDRIVE_CLIENT_SECRET}
token = {"access_token":"...","refresh_token":"...","expiry":"..."}
```

- `refresh_token` があるため、access_tokenは自動更新される
- Google OAuthのrefresh_tokenは明示的にrevokeしない限り無期限（Workspace管理者がポリシーで制限可能）
- スコープは `drive.readonly` に限定し、VMからGDriveへの書き込みは禁止

### InfraBoxへの組み込み

#### 1. ベースイメージの変更

```dockerfile
# images/base/Dockerfile に追加
RUN curl https://rclone.org/install.sh | bash

# FUSE (方式Fを将来使う場合)
# RUN apt-get update && apt-get install -y fuse3 && rm -rf /var/lib/apt/lists/*
```

#### 2. rclone設定の配布（K8s Secret経由）

```go
// api/k8s/vm.go - 新しいVolume/VolumeMountを追加

// ユーザーごとのrclone設定（OAuthトークン含む）をSecretとして配布
{
    Name: "rclone-config",
    VolumeSource: corev1.VolumeSource{
        Secret: &corev1.SecretVolumeSource{
            SecretName:  "rclone-config-" + cfg.Owner,
            DefaultMode: pointer.Int32(0400),
        },
    },
}
```

#### 3. initContainerで初回同期

```go
// api/k8s/vm.go - initContainerに追加
{
    Name:  "sync-context",
    Image: cfg.BaseImage,
    Command: []string{"bash", "-c", `
        if [ -f /etc/rclone/rclone.conf ]; then
            export RCLONE_CONFIG=/etc/rclone/rclone.conf

            mkdir -p /home/ubuntu/context
            rclone sync gdrive:"${GDRIVE_FOLDER}" /home/ubuntu/context/ \
                --include "*.md" \
                --include "*.txt" \
                --include "*.csv" \
                --transfers 8 \
                --checkers 16 \
                --log-level NOTICE
            chown -R ubuntu:ubuntu /home/ubuntu/context
        fi
    `},
    Env: []corev1.EnvVar{
        {Name: "GDRIVE_FOLDER", Value: cfg.ContextFolder},
    },
    VolumeMounts: []corev1.VolumeMount{
        {Name: "home", MountPath: "/home/ubuntu"},
        {Name: "rclone-config", MountPath: "/etc/rclone", ReadOnly: true},
    },
}
```

#### 4. 定期同期（systemd timer）

```ini
# /etc/systemd/system/context-sync.service
[Unit]
Description=Sync Google Drive context files

[Service]
Type=oneshot
User=ubuntu
Environment=RCLONE_CONFIG=/etc/rclone/rclone.conf
ExecStart=/usr/bin/rclone sync gdrive:"shared-context" /home/ubuntu/context/ \
    --include "*.md" --include "*.txt" --include "*.csv" \
    --transfers 4 --checkers 8 --log-level NOTICE
```

```ini
# /etc/systemd/system/context-sync.timer
[Unit]
Description=Periodic context sync from Google Drive

[Timer]
OnBootSec=1min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
```

#### 5. VMConfig構造体の拡張

```go
// api/k8s/vm.go
type VMConfig struct {
    // ... 既存フィールド ...
    ContextFolder string // 同期対象のGDriveフォルダパス
}
```

#### 6. API拡張 — OAuthトークンの保存

```go
// api/handlers/context.go — 新規
// POST /api/v1/context/auth — OAuthトークンをK8s Secretに保存
//
// フロー:
// 1. ib context auth → CLIがブラウザを開いてOAuth認証
// 2. リダイレクトでtokenを受け取り → InfraBox APIに送信
// 3. APIがrclone.confを生成し、K8s Secret "rclone-config-{owner}" に保存
```

#### 7. 環境変数

```bash
INFRABOX_GDRIVE_CLIENT_ID=xxxx.apps.googleusercontent.com   # OAuthクライアントID
INFRABOX_GDRIVE_CLIENT_SECRET=GOCSPX-xxxx                   # OAuthクライアントSecret
INFRABOX_GDRIVE_FOLDER=shared-context                        # 同期対象フォルダ（デフォルト）
```

### CLAUDE.mdでエージェントに検索を案内

同期されたファイルをClaude Codeが活用するために、VMの起動スクリプトで
`/home/ubuntu/context/CLAUDE.md` を自動生成する。

```markdown
# Company Context (Google Drive synced)

このディレクトリにはGoogleドライブから同期されたコンテキストファイルがあります。
あなたがアクセスできるファイルのみが同期されています。

## 検索方法
- 全文検索: `rg "キーワード" /home/ubuntu/context/`
- ファイル名検索: `fd "パターン" /home/ubuntu/context/`
- 顧客の提案書: `fd "proposal" /home/ubuntu/context/`

## 最終同期: ${LAST_SYNC_TIME}
## 手動同期: `systemctl --user start context-sync`
```

### ディレクトリ構成例

```
/home/ubuntu/
├── context/                    # GDriveから同期されたファイル
│   ├── CLAUDE.md              # エージェント向けガイド（自動生成）
│   ├── 会社概要/
│   │   ├── 会社紹介.md
│   │   └── 組織図.md
│   ├── IR/
│   │   ├── 2024-Q4決算.md
│   │   └── 2025-Q1決算.md
│   ├── 営業/
│   │   ├── 月次レポート/
│   │   └── 顧客別/
│   │       ├── A社/
│   │       │   ├── 概要.md
│   │       │   └── 提案書_2025.md
│   │       └── B社/
│   └── ナレッジ/
│       ├── 業務フロー.md
│       └── テンプレート/
└── .config/rclone/            # rclone設定（Secretからマウント）
```

## セキュリティ考慮事項

1. **OAuthトークンの管理** — ユーザーごとのK8s Secretに格納、Pod内はReadOnlyマウント（0400）
2. **トークンの自動更新** — refresh_tokenによりaccess_tokenは自動更新、ユーザー操作不要
3. **スコープの最小化** — `drive.readonly` スコープで読み取り専用に制限（VMからGDriveへの書き込み不可）
4. **権限の分離** — 各ユーザーのトークンは自分のVMにしかマウントされない
5. **ネットワーク** — rclone syncはHTTPS通信のみ
6. **ローカルコピーの暗号化** — 必要に応じてrclone crypt overlayで暗号化保存可能
7. **VM削除時** — PVCが削除されるため同期データも自動削除
8. **トークン失効** — ユーザーがGoogleアカウント側でアクセスをrevokeすれば即座に同期停止

## 段階的な導入ステップ

1. **Phase 1**: rcloneをベースイメージに追加
2. **Phase 2**: GCPでOAuthクライアントIDを作成、InfraBoxの環境変数に設定
3. **Phase 3**: `ib context auth` CLIコマンドを実装（ブラウザOAuth → トークン保存）
4. **Phase 4**: APIにトークン保存エンドポイント追加、K8s SecretとしてVM配布
5. **Phase 5**: VMのinitContainerで初回rclone sync
6. **Phase 6**: systemd timerで定期同期（5分間隔）
7. **Phase 7**: CLAUDE.md自動生成で検索ガイドを提供
