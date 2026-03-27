# exe.dev が良さそうだったので自作してみた

AI コーディングが当たり前になって、開発の速度感が変わりました。

Claude Code に「この機能を実装して」と投げれば、数分でそれなりに動くものができあがります。ローカルで動かして、確認して、「よし、誰かに見せよう」となる。そこで止まります。

「見せる」がこんなに難しかったか、と毎回思います。

Cloud Run に載せるか？でもコンテナ化されていないし、サーバーレスに書き直す必要があるかもしれない。Cloud SQL？ 従量課金の設定と、IAM 周りの整理が要る。Vercel？ Next.js じゃないと厳しい。ngrok でローカルを tunneling する？ それは一時しのぎで、翌日には URL が変わっています。

本来やりたいことは「動くものを今すぐ見せる」だけなのに、気づいたらインフラの設計をしています。

この問題、ずっと気になっていたのですが、自分の中で言語化できていませんでした。それを [exe.dev](https://exe.dev) が解いていました。

コマンド一発で Ubuntu VM が起動します。作成と同時に `https://<name>.exe.dev` という URL が払い出されます。VM 内で好きなポートを Listen するだけで、外からアクセスできます。SSH キー管理もない、TLS 証明書の設定もない、ポート公開の設定もない。月 $20 で VM 25台まで。Claude Code や Codex もそのまま動くし、そもそも「AI エージェントが使う環境」として設計されています。

見た瞬間に「これだ」と思いました。

AI コーディングが速くなればなるほど、「ローカルで動くものを外に出す」部分がボトルネックになります。exe.dev はそこにピンポイントで刺さっていました。サービスの着眼点として純粋に素晴らしいと思います。

ただ、社内で使うとなると引っかかりがあります。

エンジニアが日常的に使う環境となると、そこには API キー、社内のコード、顧客データに近いものが乗ることになります。外部 SaaS のマシンにそれを置くのは、セキュリティポリシーの観点で整理が必要です。法人契約も含めて検討しましたが、「それなら自前で作ってしまおう」という結論になりました。幸い Kubernetes と Go があれば、似たような仕組みは作れるはずです。それが InfraBox の出発点です。

---

## 何を作ったか

**InfraBox** — エンジニアがコマンド一発で隔離された Linux 環境を数秒で立ち上げられる、自社ホスティングの軽量 VM プラットフォームです。

```bash
$ ib create my-app
Ready (7s)

  Shell:     ib ssh my-app
  HTTPS URL: https://my-app.infrabox.abejatech.com
```

VM が起動すると同時に HTTPS URL が払い出されます。VM 内でポート 8000 を Listen するだけで、外部からアクセスできます。ローカルで動いているものをそのまま持ち込んで、そのまま共有できます。

ソースは公開しています: https://github.com/shogomuranushi/infrabox

---

## アーキテクチャ

シンプルに作りました。Kubernetes (k3s or GKE) の上に Go 製の API サーバーを乗せて、VM は Pod として動かします。

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

API ノードは on-demand インスタンス、VM を動かすワーカーノードはスポットインスタンスにしました。コスト効率と耐障害性のバランスを取るためです。スポットインスタンスが落ちても Pod は再スケジュールされます。PD (Persistent Disk) で永続化しているので、再起動しても状態は残ります。

---

## 作っていて工夫したこと

### SSH を捨てた

最初は sshpiper を使って SSH アクセスを実装していました。ポート 2222 を開けて、SSH キーを管理して...と、それなりに動いていました。

が、途中で「SSH、いらなくない？」と気づきました。

AWS Systems Manager Session Manager と同じ発想です。WebSocket + Kubernetes の pod exec で shell セッションを確立すれば、SSH 自体が不要になります。ポート 22/2222 を開ける必要がない。SSH キーも不要。sshpiper も不要。

```
ib ssh myvm
  → WebSocket (wss://api.example.com/v1/vms/myvm/exec)
  → API キーで認証
  → K8s pod exec (SPDY) でコンテナに接続
  → インタラクティブな bash セッション
```

PR #12 でこれを実装したとき、27 ファイルを変更して SSH 関連のコードを根こそぎ削りました。結果として、SSH キー管理の複雑さが消えて、セキュリティの攻撃面も減りました。やってよかったです。

### per-VM の認証切り替え

デフォルトは URL を知っていれば誰でもアクセスできるオープンな状態にしました。PoC を社内に共有するだけなら認証は邪魔になることが多いためです。

ただ、外部に公開するときは SSO をかけたい。そこで `ib auth enable/disable` コマンドを作って、VM ごとに Google Workspace 認証を ON/OFF できるようにしました。

```bash
ib auth enable my-app   # Google Workspace SSO を有効化
ib auth disable my-app  # 認証なしの完全オープンに戻す
```

実装は oauth2-proxy をサイドカーとして使うシンプルな構成です。

ちなみに、実装直後にセキュリティ上の問題に気づきました。クライアントが `X-Auth-Request-Email` ヘッダーを偽造して送れば、認証をバイパスできてしまいます。nginx の `configuration-snippet` でそのヘッダーを ingress レイヤーで strip するよう修正しました。細かいところですが、こういうのは後から直すより先に潰しておくほうがいいです。

### ib sync — VM を作るたびにファイルを自動転送

Claude Code を使う方なら、`~/.claude/settings.json` や `~/.claude.json` を VM に毎回コピーするのが地味に面倒だと感じると思います。

`ib sync` はそれを解決する機能です。一度登録しておけば、`ib create` のたびに自動でファイルが転送されます。

```bash
# 一度だけ登録
ib sync add ~/.claude/settings.json /home/ubuntu/.claude/settings.json
ib sync add ~/.claude.json          /home/ubuntu/.claude.json

# 以後は ib create のたびに自動転送される
ib create my-new-env
```

「ローカルの設定を VM に持ち込む」という操作は Claude Code ユーザーが毎回やることなので、これを自動化するだけでかなり体験が変わります。

### Claude Code をプリインストール

ベースイメージ (Ubuntu 24.04) に Claude Code をあらかじめインストールしてあります。VM を作ったらすぐにエージェントを使い始められます。

```bash
ib create agent-01
ib ssh agent-01
# → そのまま claude コマンドが使える
```

ちょっとハマったのが、Claude Code のネイティブインストーラーがユーザー権限でインストールしないと `~/.local/bin/claude` への自動アップデートが壊れるという点です。最初は root でインストールしていましたが、ubuntu ユーザーで実行するよう修正しました (PR #41)。細かいですが、こういうのが積み重なって体験の差になります。

---

## 現在の状況

ABEJA 社内で実際に稼働しています。GKE Standard 上に展開していて、エンジニアが PoC や実験環境として日常的に使っています。

- VM 作成: 7〜10 秒
- HTTPS URL: 作成と同時に払い出し
- リソース: CPU 0.1コア(request) / 1コア(limit)、メモリ 400MiB(request) / 2GiB(limit)
- 1人あたり最大 10 VM

request/limit を大きく分けているのは意図的で、リソース効率を上げるためです。アイドル時は小さく、スパイク時は大きく使えます。その分ノイジーネイバーの可能性はありますが、実験環境としての用途なら許容範囲だと判断しました。

---

## OSS として公開しています

https://github.com/shogomuranushi/infrabox

GCE + k3s (Terraform) でも GKE Standard (Terraform) でも動く構成にしました。`terraform apply` 一発でクラスタが立ち上がり、InfraBox が動く状態になります。

```bash
cd scripts/terraform-gce
cp terraform.tfvars.example terraform.tfvars
# gcp_project / domain / letsencrypt_email を設定
terraform apply
```

---

## 顧客への展開可能性

AI コーディングを中心に内製化を進めている企業には特に刺さると思っています。

エンジニアが Claude Code や Cursor を使って開発しているとき、「この実装を今すぐ確認してほしい」という瞬間に HTTPS URL が払い出される環境があると、開発のテンポが根本的に変わります。Slack でスクリーンショットを共有するのと、URL を投げるのとでは、フィードバックの質が全然違います。

自社環境に InfraBox を導入したい、顧客向けに展開を検討したいというご相談は CEO 室までどうぞ。

---

## まとめ

exe.dev が良さそうだったので、セキュリティ要件と自社の事情に合わせて自前で作りました。作ってみると意外とシンプルで、Kubernetes と Go と少しの OSS を組み合わせるだけで、exe.dev に近い体験が再現できました。

「SSH を捨てる」「per-VM の認証切り替え」「ファイルの自動同期」あたりが自作ならではの工夫だと思っています。

AI コーディング全盛の今、「動くものをすぐ見せる」インフラが整っていると、開発の体験が相当変わります。興味があればぜひ試してみてください。
