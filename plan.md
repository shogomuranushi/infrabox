# 実装計画: ユーザーごとNamespace分離 + ResourceQuota (2CPU/8GiB)

## 概要
現状: 全VMが共有namespace `infrabox-vms` に同居
目標: ユーザーごとに `infrabox-vms-<username>` namespaceを作成し、ResourceQuota で 2CPU / 8GiB を強制

## 変更箇所

### 1. `api/k8s/vm.go` — Namespace + ResourceQuota 管理メソッド追加
- `EnsureUserNamespace(ctx, username, resourceQuota)` メソッド追加
  - Namespace `infrabox-vms-<username>` が無ければ作成
  - ResourceQuota を作成/更新（cpu: 2, memory: 8Gi）
  - ラベル `managed-by: infrabox`, `infrabox-user: <username>` を付与
- `DeleteUserNamespace(ctx, username)` メソッド追加（ユーザーのVM全削除時用、今回は実装のみ）

### 2. `api/k8s/vm.go` — vmLabels にオーナー情報を追加
- `vmLabels(name)` → `vmLabels(name, owner)` に変更
- ラベルに `infrabox-owner: <owner>` を追加

### 3. `api/config/config.go` — ResourceQuota の設定値を追加
- `UserCPUQuota string` (env: `INFRABOX_USER_CPU_QUOTA`, default: `"2"`)
- `UserMemoryQuota string` (env: `INFRABOX_USER_MEMORY_QUOTA`, default: `"8Gi"`)

### 4. `api/k8s/vm.go` — VMConfig にオーナーフィールド追加
- `VMConfig.Owner string` フィールド追加

### 5. `api/handlers/vms.go` — Namespace を動的に決定
- `CreateVM`:
  - `h.k8s.EnsureUserNamespace()` を呼び出し
  - `Namespace` を `infrabox-vms-<owner>` に設定（admin の場合は既存の `h.cfg.VMNamespace` のまま）
  - VMConfig に Owner をセット
- `DeleteVM`: namespace を `infrabox-vms-<owner>` に
- `RestartVM`: namespace を `infrabox-vms-<owner>` に
- WaitForPodReady の namespace も同様に修正

### 6. `api/db/db.go` — VM にnamespace情報を保存
- `vms` テーブルに `namespace` カラム追加（マイグレーション）
- `VM` struct に `Namespace string` フィールド追加
- Insert/Get/List の各メソッドを更新

### 7. `k8s/rbac.yaml` — 権限追加
- `namespaces` リソースへの `create`, `get`, `list`, `delete` 権限
- `resourcequotas` リソースへの `create`, `get`, `list`, `update`, `patch` 権限

### 8. `scripts/local-setup.sh` — 初期namespace作成ロジックの説明コメント追加
- ユーザーnamespaceは動的に作られるため、初期セットアップでは `infrabox-vms` のみ作成（既存のまま）
- コメントで「ユーザーnamespaceはAPI経由で自動作成される」旨を追記

## 影響範囲
- 既存VMは `infrabox-vms` namespaceのまま（マイグレーション時にデフォルト値を設定）
- 新規VMからユーザー別namespaceに作成される
- admin (owner="") の場合は従来通り `infrabox-vms` を使用

## ResourceQuota の内容
```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: user-quota
  namespace: infrabox-vms-<username>
spec:
  hard:
    limits.cpu: "2"
    limits.memory: 8Gi
```

## VM の Resource設定の調整
- 現状の Limits (2CPU/8Gi) だと1ユーザーが1VMしか作れない
- Limits を 1CPU / 4Gi に下げて2台作れるようにするか？ → ユーザーに確認が必要かも
- ひとまず ResourceQuota だけ入れて、VM の Limits はそのままにする（結果的に1台制限になる）
