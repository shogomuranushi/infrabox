#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# InfraBox ローカル検証（VPS不要）
# 使い方:
#   ./scripts/local-setup.sh
#
# 目標: Lima + k3s で SSH・PVC・HTTPS を一気に検証する
# 所要時間: 約1時間
# ============================================================

echo "=== 0-1. Lima + k3s のセットアップ ==="

if ! command -v limactl &>/dev/null; then
  echo "--- Lima をインストール ---"
  brew install lima
fi

if limactl list -q 2>/dev/null | grep -q '^k3s$'; then
  echo "--- Lima VM 'k3s' は既に存在します。起動を確認 ---"
  limactl start k3s 2>/dev/null || true
else
  echo "--- k3s VM を作成・起動 ---"
  limactl start --name=k3s template://k3s \
    --memory=4 --disk=30 --cpus=4
fi

echo "--- kubeconfig 取得 ---"
mkdir -p ~/.kube
limactl shell k3s sudo cat /etc/rancher/k3s/k3s.yaml > ~/.kube/config-infrabox
export KUBECONFIG=~/.kube/config-infrabox

echo "--- ノード確認 ---"
kubectl get nodes
echo ""

echo "--- Traefik 無効化（nginx-ingress と競合するため） ---"
limactl shell k3s sudo sh -c \
  'grep -q "disable:.*traefik" /etc/rancher/k3s/config.yaml 2>/dev/null || \
   (echo "disable: [traefik]" >> /etc/rancher/k3s/config.yaml && systemctl restart k3s)'

echo "--- Traefik Pod が消えるまで待つ ---"
for i in $(seq 1 12); do
  if ! kubectl get pods -n kube-system 2>/dev/null | grep -q traefik; then
    echo "  Traefik 停止確認OK"
    break
  fi
  echo "  Traefik 停止待ち... (${i}/12)"
  sleep 5
done

echo "--- Namespace 作成 ---"
kubectl create namespace infrabox --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace infrabox-vms --dry-run=client -o yaml | kubectl apply -f -

echo ""
echo "=== 0-2. ベースイメージをローカルでビルド・インポート ==="
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if ! docker info &>/dev/null; then
  echo "ERROR: Docker が起動していません。Docker Desktop を起動してください。"
  exit 1
fi

echo "--- ベースイメージをビルド ---"
docker build -t infrabox-base:ubuntu-24.04 "$REPO_ROOT/images/base/"

echo "--- k3s にインポート（レジストリ不要） ---"
docker save infrabox-base:ubuntu-24.04 | \
  limactl shell k3s sudo k3s ctr images import -

echo "--- インポート確認 ---"
limactl shell k3s sudo k3s ctr images list | grep infrabox-base

echo ""
echo "=== 0-3. SSH 検証（①相当） ==="

echo "--- テスト用 VM Pod 起動 ---"
kubectl apply -n infrabox-vms -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: vm-test
  labels:
    app: vm-test
spec:
  containers:
    - name: vm
      image: docker.io/library/infrabox-base:ubuntu-24.04
      imagePullPolicy: Never
      ports:
        - containerPort: 22
---
apiVersion: v1
kind: Service
metadata:
  name: vm-test-svc
spec:
  selector:
    app: vm-test
  ports:
    - port: 22
      targetPort: 22
EOF

echo "--- Pod Ready 待ち ---"
kubectl wait --for=condition=Ready pod/vm-test -n infrabox-vms --timeout=120s

echo ""
echo "--- sshpiper upstream 用キーペア生成 ---"
if [ ! -f ~/.ssh/id_ed25519.pub ]; then
  echo "ERROR: ~/.ssh/id_ed25519.pub が見つかりません。SSH鍵を作成してください。"
  exit 1
fi
PUB_KEY=$(awk '{$1=$1; print}' ~/.ssh/id_ed25519.pub)

UPSTREAM_KEY_FILE=$(mktemp /tmp/sshpiper_upstream.XXXXXX)
ssh-keygen -t ed25519 -f "$UPSTREAM_KEY_FILE" -N ""
UPSTREAM_PUB=$(cat "${UPSTREAM_KEY_FILE}.pub")

kubectl create secret generic sshpiper-upstream-key \
  --from-file=ssh-privatekey="$UPSTREAM_KEY_FILE" \
  -n infrabox \
  --dry-run=client -o yaml | kubectl apply -f -

rm -f "$UPSTREAM_KEY_FILE" "${UPSTREAM_KEY_FILE}.pub"

echo "--- vm-test に upstream 公開鍵を登録 ---"
kubectl exec -n infrabox-vms vm-test -- bash -c "
  mkdir -p /home/ubuntu/.ssh
  chmod 700 /home/ubuntu/.ssh
  chown ubuntu:ubuntu /home/ubuntu/.ssh
  echo '$UPSTREAM_PUB' > /home/ubuntu/.ssh/authorized_keys
  chmod 600 /home/ubuntu/.ssh/authorized_keys
  chown ubuntu:ubuntu /home/ubuntu/.ssh/authorized_keys
"

echo ""
echo "--- sshpiper インストール ---"
helm repo add sshpiper https://tg123.github.io/sshpiper-chart/ 2>/dev/null || true
helm repo update

if helm status sshpiper -n infrabox &>/dev/null; then
  echo "  sshpiper は既にインストール済み"
else
  helm install sshpiper sshpiper/sshpiper \
    --namespace infrabox \
    --set service.type=ClusterIP
fi

echo "--- sshpiper Pod Ready 待ち ---"
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=sshpiper \
  -n infrabox --timeout=120s

echo ""
echo "--- Pipe CRD 作成（sshpiper v1.5+ の from/to 形式） ---"
kubectl apply -n infrabox -f - <<EOF
apiVersion: sshpiper.com/v1beta1
kind: Pipe
metadata:
  name: vm-test
spec:
  from:
    - username: "vm-test"
      authorized_keys_data: "${PUB_KEY}"
  to:
    host: "vm-test-svc.infrabox-vms.svc.cluster.local:22"
    username: "ubuntu"
    ignore_hostkey: true
    private_key_secret:
      name: sshpiper-upstream-key
EOF

echo ""
echo "--- port-forward 起動（Lima VM NodePort は Mac から直接到達不可） ---"
pkill -f "kubectl.*port-forward svc/sshpiper" 2>/dev/null || true
sleep 1
kubectl port-forward svc/sshpiper 2222:2222 -n infrabox > /tmp/infrabox-sshpiper-pf.log 2>&1 &
SSHPIPER_PF_PID=$!
sleep 3

echo ""
echo "--- ✅ チェック1: SSH 接続テスト ---"
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=15"
SSH_RESULT=$(ssh -p 2222 $SSH_OPTS vm-test@127.0.0.1 "whoami" 2>/dev/null || true)
if [ "$SSH_RESULT" = "ubuntu" ]; then
  echo "  ✅ チェック1: SSH 接続OK（ユーザー: $SSH_RESULT）"
else
  echo "  ❌ チェック1 失敗: SSH 接続できませんでした"
  echo "  デバッグ: kubectl logs -l app.kubernetes.io/name=sshpiper -n infrabox"
  exit 1
fi

echo ""
echo "=== 0-4. 永続ディスク検証（②相当） ==="

echo "--- PVC + Pod 作成 ---"
kubectl apply -n infrabox-vms -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-test
spec:
  storageClassName: local-path
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 10Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: vm-persistent
  labels:
    app: vm-persistent
spec:
  containers:
    - name: vm
      image: docker.io/library/infrabox-base:ubuntu-24.04
      imagePullPolicy: Never
      ports:
        - containerPort: 22
      volumeMounts:
        - name: home
          mountPath: /home/ubuntu
  volumes:
    - name: home
      persistentVolumeClaim:
        claimName: pvc-test
---
apiVersion: v1
kind: Service
metadata:
  name: vm-persistent-svc
spec:
  selector:
    app: vm-persistent
  ports:
    - port: 22
      targetPort: 22
EOF

echo "--- Pod Ready 待ち ---"
kubectl wait --for=condition=Ready pod/vm-persistent -n infrabox-vms --timeout=60s

echo ""
echo "--- vm-persistent に upstream 公開鍵を登録 ---"
# PVCマウント後 /home/ubuntu が root 所有 + world-writable になるため修正が必要
kubectl exec -n infrabox-vms vm-persistent -- bash -c "
  chown ubuntu:ubuntu /home/ubuntu
  chmod 750 /home/ubuntu
  mkdir -p /home/ubuntu/.ssh
  chmod 700 /home/ubuntu/.ssh
  chown ubuntu:ubuntu /home/ubuntu/.ssh
  echo '$UPSTREAM_PUB' > /home/ubuntu/.ssh/authorized_keys
  chmod 600 /home/ubuntu/.ssh/authorized_keys
  chown ubuntu:ubuntu /home/ubuntu/.ssh/authorized_keys
"

echo "--- sshpiper Pipe 追加 ---"
kubectl apply -n infrabox -f - <<EOF
apiVersion: sshpiper.com/v1beta1
kind: Pipe
metadata:
  name: vm-persistent
spec:
  from:
    - username: "vm-persistent"
      authorized_keys_data: "${PUB_KEY}"
  to:
    host: "vm-persistent-svc.infrabox-vms.svc.cluster.local:22"
    username: "ubuntu"
    ignore_hostkey: true
    private_key_secret:
      name: sshpiper-upstream-key
EOF

echo ""
echo "--- ファイル書き込み ---"
ssh -p 2222 $SSH_OPTS vm-persistent@127.0.0.1 \
  "echo 'persistence test' > ~/testfile.txt && cat ~/testfile.txt"

echo ""
echo "--- Pod 削除（PVCは残る） ---"
kubectl delete pod vm-persistent -n infrabox-vms
sleep 2

echo "--- Pod 再作成（同じPVCをマウント） ---"
kubectl apply -n infrabox-vms -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: vm-persistent
  labels:
    app: vm-persistent
spec:
  containers:
    - name: vm
      image: docker.io/library/infrabox-base:ubuntu-24.04
      imagePullPolicy: Never
      ports:
        - containerPort: 22
      volumeMounts:
        - name: home
          mountPath: /home/ubuntu
  volumes:
    - name: home
      persistentVolumeClaim:
        claimName: pvc-test
EOF

kubectl wait --for=condition=Ready pod/vm-persistent -n infrabox-vms --timeout=60s

echo ""
echo "--- ファイル残存確認 ---"
RESULT=$(ssh -p 2222 $SSH_OPTS vm-persistent@127.0.0.1 "cat ~/testfile.txt" 2>/dev/null)

if [ "$RESULT" = "persistence test" ]; then
  echo "  ✅ チェック2: ファイルが永続化されています（$RESULT）"
else
  echo "  ❌ チェック2 失敗: 期待='persistence test' 実際='$RESULT'"
  exit 1
fi

echo ""
echo "=== 0-5. HTTPS 公開検証（③相当） ==="

echo "--- cert-manager インストール ---"
helm repo add jetstack https://charts.jetstack.io 2>/dev/null || true
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx 2>/dev/null || true
helm repo update

if helm status cert-manager -n cert-manager &>/dev/null; then
  echo "  cert-manager は既にインストール済み"
else
  helm install cert-manager jetstack/cert-manager \
    --namespace cert-manager \
    --create-namespace \
    --set crds.enabled=true
fi

echo "--- cert-manager Ready 待ち ---"
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=cert-manager \
  -n cert-manager --timeout=120s

echo ""
echo "--- nginx-ingress インストール ---"
if helm status ingress-nginx -n ingress-nginx &>/dev/null; then
  echo "  ingress-nginx は既にインストール済み"
else
  helm install ingress-nginx ingress-nginx/ingress-nginx \
    --namespace ingress-nginx \
    --create-namespace \
    --set controller.hostPort.enabled=true \
    --set controller.service.type=NodePort \
    --set controller.kind=DaemonSet
fi

echo "--- nginx-ingress Ready 待ち ---"
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=ingress-nginx \
  -n ingress-nginx --timeout=120s

echo ""
echo "--- 自己署名 ClusterIssuer 作成 ---"
kubectl apply -f - <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
EOF

echo ""
echo "--- Pod 内で Web サーバー起動 ---"
ssh -p 2222 $SSH_OPTS vm-persistent@127.0.0.1 \
  "nohup python3 -m http.server 8000 > /tmp/http.log 2>&1 &"

HOSTNAME="vm-persistent.test.local"

echo ""
echo "--- Service + Ingress 作成 ---"
kubectl apply -n infrabox-vms -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: vm-persistent-http-svc
spec:
  selector:
    app: vm-persistent
  ports:
    - port: 8000
      targetPort: 8000
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: vm-persistent-ingress
  annotations:
    cert-manager.io/cluster-issuer: selfsigned
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - $HOSTNAME
      secretName: vm-persistent-tls
  rules:
    - host: $HOSTNAME
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: vm-persistent-http-svc
                port:
                  number: 8000
EOF

echo ""
echo "--- 証明書発行待ち ---"
for i in $(seq 1 20); do
  READY=$(kubectl get certificate -n infrabox-vms -o jsonpath='{.items[0].status.conditions[0].status}' 2>/dev/null || true)
  if [ "$READY" = "True" ]; then
    echo "  証明書 Ready"
    break
  fi
  echo "  証明書発行待ち... (${i}/20)"
  sleep 5
done

echo ""
echo "--- nginx-ingress port-forward 起動 ---"
pkill -f "kubectl.*port-forward svc/ingress-nginx" 2>/dev/null || true
sleep 1
kubectl port-forward svc/ingress-nginx-controller 8443:443 -n ingress-nginx > /tmp/infrabox-nginx-pf.log 2>&1 &
sleep 3

echo ""
echo "--- HTTPS アクセス確認 ---"
# Lima VM NodePort は Mac から直接到達不可のため port-forward + --resolve を使用
HTTP_STATUS=$(curl -sk -o /dev/null -w '%{http_code}' \
  --resolve "${HOSTNAME}:8443:127.0.0.1" "https://${HOSTNAME}:8443/")

echo ""
if [ "$HTTP_STATUS" = "200" ]; then
  echo "============================================"
  echo " ✅ チェック3: HTTPS でアクセスできました"
  echo "    URL: https://$HOSTNAME:8443/ (port-forward経由)"
  echo "============================================"
else
  echo "============================================"
  echo " ❌ チェック3 失敗: HTTP Status $HTTP_STATUS"
  echo "============================================"
  echo ""
  echo " デバッグ:"
  echo "   kubectl get certificate -n infrabox-vms"
  echo "   kubectl describe ingress vm-persistent-ingress -n infrabox-vms"
  exit 1
fi

echo ""
echo "============================================"
echo " ✅ Mac ローカル検証 完了！"
echo "============================================"
echo ""
echo " ✅ チェック1: SSH 接続OK"
echo " ✅ チェック2: PVC 永続化OK"
echo " ✅ チェック3: HTTPS 公開OK"
echo ""
echo " port-forward プロセス（バックグラウンド実行中）:"
echo "   sshpiper : localhost:2222"
echo "   nginx    : localhost:8443"
echo " 停止するには: pkill -f 'kubectl.*port-forward'"
echo ""
echo " 次のステップ:"
echo "   VPS を借りて ①〜③ を本番相当の環境で再実行し、④以降に進む。"
echo ""
echo " クリーンアップするには:"
echo "   ./scripts/local-cleanup.sh"
echo ""
