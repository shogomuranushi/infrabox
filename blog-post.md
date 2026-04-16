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

用途は PoC の共有だけではありません。VM は削除しない限りずっと動き続けるので、Cron ジョブや Slack bot のバックエンドとしても使えます。「定期的に何かを叩くスクリプト」「Slack のメッセージを受け取って処理するサーバー」を動かすのに、わざわざインフラを用意する必要がなくなります。

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

AWS Systems Manager Session Manager と同じ発想です。WebSocket + Kubernetes の pod exec でシェルセッションを確立すれば、SSH 自体が不要になります。ポート 22/2222 を開ける必要がない。SSH キーも不要。sshpiper も不要。

```
ib ssh myvm
  → WebSocket (wss://api.example.com/v1/vms/myvm/exec)
  → API キーで認証
  → K8s pod exec (SPDY) でコンテナに接続
  → インタラクティブな bash セッション
```

PR #12 でこれを実装したとき、27 ファイルを変更して SSH 関連のコードを根こそぎ削りました。結果として、SSH キー管理の複雑さが消えて、セキュリティの攻撃面も減りました。

ついでに `ib scp` も SSH に依存しないつくりにしています。ファイル転送は SCP バイナリを呼ぶのではなく、ローカルのファイルを tar でまとめて API に HTTP POST するだけです。ダウンロードも同様で、API が tar ストリームを返してくれるのをクライアント側で展開します。SCP バイナリも不要で、エンドポイントは HTTPS の 443 番だけで完結します。ちなみにダウンロード時は tar エントリのパスに `..` や絶対パスが含まれないかを検証していて、パストラバーサル攻撃を弾くようにしています。

### WebSocket を使うと nginx に殺される

WebSocket に切り替えてから、しばらく使っていると「接続が切れる」という報告が来ました。原因を調べると、nginx の idle タイムアウトでした。

nginx はデフォルトで一定時間アクティビティがない WebSocket 接続を強制的に切断します。作業に集中して長時間ターミナルを触っていない、というだけで接続が落ちる。これは困ります。

対策として、サーバー側から 30 秒ごとに WebSocket の ping を送るようにしました。これで nginx に「この接続は生きている」と認識させます。あわせて、8 時間 stdin のアクティビティがなければアイドルとみなしてセッションを閉じるタイムアウトも実装しました。

```go
const (
    sshPingInterval = 30 * time.Second
    sshIdleTimeout  = 8 * time.Hour
)
```

「30 秒 ping で接続維持、8 時間無操作で切断」というシンプルなルールです。

### スポットインスタンスは落ちる前提で作る

ワーカーノードはコスト削減のためスポットインスタンスを使っています。スポットは安い代わりに GCP の都合でいつでも preemption されます。

問題は、preemption されて再起動したあと k3s を再インストールすると `/etc/rancher/node/password` が再生成されてしまう点です。サーバー側にはすでにそのノードの password が記録されているので、「Node password rejected」エラーになってクラスタに復帰できなくなります。

対策は「k3s が既にインストールされていたら再インストールをスキップする」という一行です。

```bash
if [ -f /usr/local/bin/k3s ]; then
  log "k3s already installed, skipping setup"
  exit 0
fi
```

さらに厄介なのが MIG (Managed Instance Group) の recreation です。GCP が MIG のインスタンスを作り直すとき、同じホスト名のノードが新しい password で再登録しようとして衝突します。これは `--with-node-id` オプションで解決しました。hostname に UUID を付加して、再作成のたびにユニークなノード名を生成します。古いノードエントリはクリーンアップのために API サーバー側の cron で定期削除しています。

### per-VM の認証切り替えと、うっかりセキュリティホール

デフォルトは URL を知っていれば誰でもアクセスできるオープンな状態にしました。PoC を社内に共有するだけなら認証は邪魔になることが多いためです。

ただ、外部に公開するときは SSO をかけたい。そこで `ib auth enable/disable` コマンドを作って、VM ごとに Google Workspace 認証を ON/OFF できるようにしました。

```bash
ib auth enable my-app   # Google Workspace SSO を有効化
ib auth disable my-app  # 認証なしの完全オープンに戻す
```

実装直後に気づいたのですが、クライアントが `X-Auth-Request-Email` ヘッダーを偽造して送れば認証をバイパスできてしまいます。oauth2-proxy が認証後にセットするヘッダーを、クライアントが先に偽造して送れば、認証済みユーザーとして扱われてしまう。nginx の `configuration-snippet` で ingress レイヤーでそのヘッダーを strip するよう修正しました。oauth2-proxy より手前でヘッダーを消す、という構成です。

### クラウド依存をゼロにする

HTTPS の終端と証明書管理に、GKE のマネージドロードバランサーや AWS の ALB + ACM ではなく、**nginx-ingress + cert-manager + Let's Encrypt** の組み合わせを使っています。

GKE 専用の機能に依存すれば設定はシンプルになりますが、そのぶん他の環境に持ち込めなくなります。nginx-ingress と cert-manager は Kubernetes さえあればどこでも動くので、GKE でも GCE + k3s でも、AWS や Azure でも、オンプレでも、同じ構成でそのまま動きます。実際に InfraBox は GKE と k3s の両方に対応しており、Terraform の変数を切り替えるだけでどちらにもデプロイできます。

「どの会社のクラウドを使っているか」に関係なく導入できる、というのは顧客環境への展開を考えると重要な設計判断です。

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

当初はネイティブインストーラーで入れていたのですが、Docker ビルド中に静かに失敗するケースがあったため、`npm install -g @anthropic-ai/claude-code` に切り替えました。こちらのほうが `/usr/local/bin/claude` に確実に入ります。

シェルは zsh を採用しています。bash だと矢印キーによるコマンド履歴が効かないという地味な問題があったためです。あわせて `HISTSIZE` / `SAVEHIST` も設定してあるので、VM を再起動しても履歴が残ります。

`ib ssh` で接続すると、tmux セッション（`main`）に自動アタッチします。ターミナルを閉じて再接続しても、実行中のプロセスや画面の状態がそのまま残っています。複数ターミナルから同じ VM に入りたいときは `-s` フラグで独立したセッションも作れます。

```bash
ib ssh my-app -s work   # 別セッションで接続
```

cron もベースイメージに含まれており、コンテナ起動時に自動で立ち上がります。定期バッチを動かすのにサーバーレス化は不要です。

### Claude Code の SSH リモート接続に対応

`ib ssh-proxy` コマンドを追加しました。これにより、Claude Code の「SSH リモート」機能で InfraBox の VM に直接接続できるようになります。

仕組みはシンプルで、`ib ssh-proxy` が SSH の ProxyCommand として動作し、SSH プロトコルを既存の WebSocket exec エンドポイントに橋渡しします。VM 側に sshd は不要です。

```
~/.ssh/config に1行追加するだけで使えます:

  Host infrabox-*
    User ubuntu
    ProxyCommand ib ssh-proxy %h
```

この設定を入れると、Claude Code の SSH リモートから `ubuntu@infrabox-<vmname>` で接続できます。ローカルの IDE のフル機能を使いながら、実行環境は InfraBox の VM という構成が作れます。

ポッドを再起動するたびに SSH の known_hosts が変わって警告が出る問題は、`~/.ib/ssh_host_key` に Ed25519 の永続ホストキーを持たせることで解決しています。

### ターミナルへの貼り付けでファイルを自動アップロード

`ib ssh` に `--auto-upload` フラグを追加しました。有効にしておくと、ターミナルへの貼り付けに含まれるローカルのファイルパスを検知して、VM に自動でアップロードします。

Claude Code を VM で使っていると「このスクリーンショットを見てほしい」という場面がよくあります。従来は `ib scp` でアップロードしてからパスを伝える必要がありましたが、このフラグを使えばファイルをターミナルにドラッグ＆ドロップするだけで完結します。

セキュリティには気を遣っています。検知はブラケテッドペーストの範囲に限定していて、通常のキー入力は対象外です。アップロード元は `$HOME` 配下のみ、`.ssh` / `.aws` / `.gnupg` などは除外、拡張子も画像・PDF・テキスト系に限定、1 ファイル 20 MiB まで、という制限があります。また、パスを検知するたびに `/dev/tty` で確認を求めるので意図しないアップロードは起きません。アップロードの記録は `~/.ib/auto-upload.log` に残ります。


---

## Kubernetes だからこそできたこと

InfraBox を作っていて、「これ Kubernetes なしだと自前実装が大変だったな」と感じた部分がいくつかあります。

**「VM を作る」= 4つのリソースを作るだけ**

`ib create` の内部では Kubernetes の Deployment・PVC・Service・Ingress を順番に作っているだけです。削除も同じで、4つを消すだけ。VM の状態管理（起動しているか、落ちていないか）は Deployment コントローラーが面倒を見てくれます。`ib restart` に至っては Pod を delete するだけで、Deployment が自動で新しい Pod を立ち上げてくれます。自前で VM ライフサイクルを管理するコードをゼロから書いていたら、それだけで相当なコード量になっていたと思います。

**HTTPS URL が「勝手に」生えてくる**

`ib create` で Ingress リソースを作ると、cert-manager が annotation を検知して自動で Let's Encrypt の TLS 証明書を取得します。nginx-ingress はその証明書を使ってルーティングを設定します。InfraBox API は「Ingress を作った」という事実を K8s に伝えるだけで、証明書の取得・更新・ルーティングの設定はすべて K8s 側のコントローラーがやってくれます。

**認証の ON/OFF = annotation を1行書き換えるだけ**

`ib auth enable my-app` の実装は、Ingress リソースの annotation に oauth2-proxy の設定を追記するだけです。nginx-ingress はその変更を即座に拾って認証を有効にします。`ib auth disable` は annotation を削除するだけ。Ingress リソースが「設定の SSoT（唯一の信頼できる情報源）」として機能しているので、API 側はステートをほとんど持たなくて済んでいます。

**ユーザーごとの Namespace + ResourceQuota で多テナント分離**

ユーザーが最初に VM を作るとき、そのユーザー専用の Kubernetes Namespace を作り、ResourceQuota でリソース上限を設定します。あるユーザーが VM を作りすぎても、quota を超えた時点で K8s がブロックするので、ほかのユーザーやシステムへの影響を遮断できます。Pod の exec も Namespace をまたげないので、ユーザー間の分離はかなり強固です。これを Docker だけで実現しようとすると、リソース管理・分離ともに相当作り込みが必要になります。

**Pod の Watch API で起動完了を検知**

`ib create` は VM の起動を待ってから返します。その実装は K8s の Watch API を使っていて、Pod の状態変化をイベントとして受け取ります。ポーリングではないので、起動した瞬間に検知できます。

---

## 現在の状況

ABEJA 社内で実際に稼働しています。GKE Standard 上に展開していて、エンジニアが PoC や実験環境として日常的に使っています。

- VM 作成: 7〜10 秒
- HTTPS URL: 作成と同時に払い出し
- 1人あたり最大 10 VM

**Kubernetes の Requests/Limits でリソース効率を上げる**

VM のリソース設定を少し説明します。Kubernetes にはリソースの「Request（予約値）」と「Limit（上限値）」という概念があります。

Request はスケジューリング用の予約で、この値を満たせるノードにしか Pod が配置されません。Limit は実際に使える上限で、これを超えると CPU はスロットリング、メモリはプロセスキルが走ります。

InfraBox では Request と Limit を意図的に大きく乖離させています。

```
CPU:    Request 0.1コア  /  Limit 1コア  （10倍）
Memory: Request 400MiB  /  Limit 2GiB   （5倍）
```

ふつうの VM サービスであれば「1コアのマシン」を1台借りたら、使っていなくても1コア分のリソースが専有されます。InfraBox の場合、アイドル中の VM は 0.1コアぶんしか予約しないため、同じノードに10倍の VM を詰め込めます。コードを書いていないとき、ターミナルを開きっぱなしにしているだけのとき、VM は CPU をほとんど使いません。その空きをほかの VM が使える、という設計です。

これは Cron や Slack bot 用途にも効いています。「何かをトリガーに動く」系のプロセスは、ほとんどの時間を待機に費やします。0.1コアの予約で常時稼働させておき、イベントが来たときだけ 1コアまでスパイクする、というのはこの用途にぴったりです。Lambda のようなサーバーレスで書き直す必要もなく、ふつうの常駐プロセスをそのまま動かせます。

トレードオフとして「ノイジーネイバー」が発生する可能性はあります。同じノードの誰かが CPU を大量消費していると、自分の VM が遅くなることがあります。実験環境としての用途では許容範囲だと判断しましたが、本番サービスを動かすような用途には向いていません。

---

## OSS として公開しています

https://github.com/shogomuranushi/infrabox

GKE Standard (Terraform) で動く構成にしました。`terraform apply` 一発でクラスタが立ち上がり、InfraBox が動く状態になります。

```bash
cd scripts/terraform-gke
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

「SSH を捨てる」「per-VM の認証切り替え」「ファイルの自動同期」に加えて、最近では「Claude Code SSH リモート対応」「ターミナル貼り付けでファイル自動アップロード」あたりが自作ならではの工夫だと思っています。

AI コーディング全盛の今、「動くものをすぐ見せる」インフラが整っていると、開発の体験が相当変わります。興味があればぜひ試してみてください。
