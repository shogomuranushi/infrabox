# exe.dev が良さそうだったので自作してみた

AI コーディングが当たり前になって、開発の速度感が変わった。

Claude Code に「この機能を実装して」と投げれば、数分でそれなりに動くものができあがる。ローカルで動かして、確認して、「よし、誰かに見せよう」となる。そこで止まる。

「見せる」がこんなに難しかったか、と毎回思う。

Cloud Run に載せるか？でもコンテナ化されていないし、サーバーレスに書き直す必要があるかもしれない。Cloud SQL？ 従量課金の設定と、IAM 周りの整理が要る。Vercel？ Next.js じゃないと厳しい。ngrok でローカルを tunneling する？ それは一時しのぎで、翌日には URL が変わっている。

本来やりたいことは「動くものを今すぐ見せる」だけなのに、気づいたらインフラの設計をしている。

この問題、ずっと気になっていたのだが、自分の中で言語化できていなかった。それを [exe.dev](https://exe.dev) が解いていた。

コマンド一発で Ubuntu VM が起動する。作成と同時に `https://<name>.exe.dev` という URL が払い出される。VM 内で好きなポートを Listen するだけで、外からアクセスできる。SSH キー管理もない、TLS 証明書の設定もない、ポート公開の設定もない。月 $20 で VM 25台まで。Claude Code や Codex もそのまま動くし、そもそも「AI エージェントが使う環境」として設計されている。

見た瞬間に「これだ」と思った。

AI コーディングが速くなればなるほど、「ローカルで動くものを外に出す」部分がボトルネックになる。exe.dev はそこにピンポイントで刺さっていた。サービスの着眼点として純粋に素晴らしいと思う。

ただ、社内で使うとなると引っかかりがある。

エンジニアが日常的に使う環境となると、そこには API キー、社内のコード、顧客データに近いものが乗ることになる。外部 SaaS のマシンにそれを置くのは、セキュリティポリシーの観点で整理が必要だ。法人契約も含めて検討したが、「それなら自前で作ってしまおう」という結論になった。幸い Kubernetes と Go があれば、似たような仕組みは作れるはずだ。それが InfraBox の出発点だ。

---

## 何を作ったか

**InfraBox** — エンジニアがコマンド一発で隔離された Linux 環境を数秒で立ち上げられる、自社ホスティングの軽量 VM プラットフォームだ。

```bash
$ ib create my-app
Ready (7s)

  Shell:     ib ssh my-app
  HTTPS URL: https://my-app.infrabox.abejatech.com
```

VM が起動すると同時に HTTPS URL が払い出される。VM 内でポート 8000 を Listen するだけで、外部からアクセスできる。ローカルで動いているものをそのまま持ち込んで、そのまま共有できる。

ソースは公開している: https://github.com/shogomuranushi/infrabox

---

## アーキテクチャ

シンプルに作った。Kubernetes (k3s or GKE) の上に Go 製の API サーバーを乗せて、VM は Pod として動かす。

```
User
  └─ ib ssh / https://my-app.infrabox.example.com
       │ HTTPS:443
       ▼
  Kubernetes Cluster
  ┌─────────────────────────────┐
  │ API Node (on-demand)        │
  │  InfraBox API (Go/Gin)      │
  │  nginx-ingress + cert-manager│
  └─────────────────────────────┘
  ┌─────────────────────────────┐
  │ Worker Node (spot)          │
  │  VM Pods (Ubuntu 24.04)     │
  │  per-user namespace         │
  └─────────────────────────────┘
```

API ノードは on-demand インスタンス、VM を動かすワーカーノードはスポットインスタンスにした。コスト効率と耐障害性のバランスを取るためだ。スポットインスタンスが落ちても Pod は再スケジュールされる。PD (Persistent Disk) で永続化しているので、再起動しても状態は残る。

---

## 作っていて工夫したこと

### SSH を捨てた

最初は sshpiper を使って SSH アクセスを実装していた。ポート 2222 を開けて、SSH キーを管理して...と、それなりに動いていた。

が、途中で「SSH、いらなくない？」と気づいた。

AWS Systems Manager Session Manager と同じ発想だ。WebSocket + Kubernetes の pod exec で shell セッションを確立すれば、SSH 自体が不要になる。ポート 22/2222 を開ける必要がない。SSH キーも不要。sshpiper も不要。

```
ib ssh myvm
  → WebSocket (wss://api.example.com/v1/vms/myvm/exec)
  → API キーで認証
  → K8s pod exec (SPDY) でコンテナに接続
  → インタラクティブな bash セッション
```

PR #12 でこれを実装したとき、27 ファイルを変更して SSH 関連のコードを根こそぎ削った。結果として、SSH キー管理の複雑さが消えて、セキュリティの攻撃面も減った。やってよかった。

### per-VM の認証切り替え

デフォルトは URL を知っていれば誰でもアクセスできるオープンな状態にした。PoC を社内に共有するだけなら認証は邪魔になることが多い。

ただ、外部に公開するときは SSO をかけたい。そこで `ib auth enable/disable` コマンドを作って、VM ごとに Google Workspace 認証を ON/OFF できるようにした。

```bash
ib auth enable my-app   # Google Workspace SSO を有効化
ib auth disable my-app  # 認証なしの完全オープンに戻す
```

実装は oauth2-proxy をサイドカーとして使うシンプルな構成だ。

ちなみに、実装直後にセキュリティ上の問題に気づいた。クライアントが `X-Auth-Request-Email` ヘッダーを偽造して送れば、認証をバイパスできてしまう。nginx の `configuration-snippet` でそのヘッダーを ingress レイヤーで strip するよう修正した。細かいところだが、こういうのは後から直すより先に潰しておくほうがいい。

### ib sync — VM を作るたびにファイルを自動転送

Claude Code を使う人なら、`~/.claude/settings.json` や `~/.claude.json` を VM に毎回コピーするのが地味に面倒だと感じると思う。

`ib sync` はそれを解決する機能だ。一度登録しておけば、`ib create` のたびに自動でファイルが転送される。

```bash
# 一度だけ登録
ib sync add ~/.claude/settings.json /home/ubuntu/.claude/settings.json
ib sync add ~/.claude.json          /home/ubuntu/.claude.json

# 以後は ib create のたびに自動転送される
ib create my-new-env
```

「ローカルの設定を VM に持ち込む」という操作は Claude Code ユーザーが毎回やることなので、これを自動化するだけでかなり体験が変わる。

### Claude Code をプリインストール

ベースイメージ (Ubuntu 24.04) に Claude Code をあらかじめインストールしてある。VM を作ったらすぐにエージェントを使い始められる。

```bash
ib create agent-01
ib ssh agent-01
# → そのまま claude コマンドが使える
```

ちょっとハマったのが、Claude Code のネイティブインストーラーがユーザー権限でインストールしないと `~/.local/bin/claude` への自動アップデートが壊れるという点だ。最初は root でインストールしていたが、ubuntu ユーザーで実行するよう修正した (PR #41)。細かいけど、こういうのが積み重なって体験の差になる。

---

## 現在の状況

ABEJA 社内で実際に稼働中だ。GKE Standard 上に展開していて、エンジニアが PoC や実験環境として日常的に使っている。

- VM 作成: 7〜10 秒
- HTTPS URL: 作成と同時に払い出し
- リソース: CPU 0.1コア(request) / 1コア(limit)、メモリ 400MiB(request) / 2GiB(limit)
- 1人あたり最大 10 VM

request/limit を大きく分けているのは意図的で、リソース効率を上げるためだ。アイドル時は小さく、スパイク時は大きく使える。その分ノイジーネイバーの可能性はあるが、実験環境としての用途なら許容範囲だと判断した。

---

## OSS として公開している

https://github.com/shogomuranushi/infrabox

GCE + k3s (Terraform) でも GKE Standard (Terraform) でも動く構成にした。`terraform apply` 一発でクラスタが立ち上がり、InfraBox が動く状態になる。

```bash
cd scripts/terraform-gce
cp terraform.tfvars.example terraform.tfvars
# gcp_project / domain / letsencrypt_email を設定
terraform apply
```

---

## 顧客への展開可能性

AIコーディングを中心に内製化を進めている企業には特に刺さると思っている。

エンジニアが Claude Code や Cursor を使って開発しているとき、「この実装を今すぐ確認してほしい」という瞬間に HTTPS URL が払い出される環境があると、開発のテンポが根本的に変わる。Slack でスクリーンショットを共有するのと、URL を投げるのとでは、フィードバックの質が全然違う。

自社環境に InfraBox を導入したい、顧客向けに展開を検討したいというご相談は CEO 室までどうぞ。

---

## まとめ

exe.dev が良さそうだったので、セキュリティ要件と自社の事情に合わせて自前で作った。作ってみると意外とシンプルで、Kubernetes と Go と少しの OSS を組み合わせるだけで、exe.dev に近い体験が再現できた。

「SSH を捨てる」「per-VM の認証切り替え」「ファイルの自動同期」あたりが自作ならではの工夫だと思っている。

AI コーディング全盛の今、「動くものをすぐ見せる」インフラが整っていると、開発の体験が相当変わる。興味があればぜひ試してほしい。
