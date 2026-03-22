#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# InfraBox ローカル検証（VPS不要）
# 使い方:
#   ./scripts/local-setup.sh
#
# 目標: Lima + k3s で PVC・HTTPS を一気に検証する
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
echo "=== 0-3. exec 検証（WebSocket exec） ==="

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
      command: ["sleep", "infinity"]
      ports:
        - containerPort: 8000
EOF

echo "--- Pod Ready 待ち ---"
kubectl wait --for=condition=Ready pod/vm-test -n infrabox-vms --timeout=120s

echo ""
echo "--- exec 検証 ---"
EXEC_RESULT=$(kubectl exec -n infrabox-vms vm-test -c vm -- whoami 2>/dev/null || true)
if [ "$EXEC_RESULT" = "root" ]; then
  echo "  exec OK（ユーザー: $EXEC_RESULT）"
else
  echo "  exec 失敗"
  echo "  デバッグ: kubectl describe pod vm-test -n infrabox-vms"
  exit 1
fi

echo ""
echo "=== 0-4. 永続ディスク検証 ==="

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
      command: ["sleep", "infinity"]
      ports:
        - containerPort: 8000
      volumeMounts:
        - name: home
          mountPath: /home/ubuntu
  volumes:
    - name: home
      persistentVolumeClaim:
        claimName: pvc-test
EOF

echo "--- Pod Ready 待ち ---"
kubectl wait --for=condition=Ready pod/vm-persistent -n infrabox-vms --timeout=60s

echo ""
echo "--- ファイル書き込み ---"
kubectl exec -n infrabox-vms vm-persistent -c vm -- \
  bash -c "chown ubuntu:ubuntu /home/ubuntu && su - ubuntu -c 'echo persistence\ test > ~/testfile.txt && cat ~/testfile.txt'"

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
      command: ["sleep", "infinity"]
      ports:
        - containerPort: 8000
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
RESULT=$(kubectl exec -n infrabox-vms vm-persistent -c vm -- cat /home/ubuntu/testfile.txt 2>/dev/null)

if [ "$RESULT" = "persistence test" ]; then
  echo "  ファイルが永続化されています（$RESULT）"
else
  echo "  失敗: 期待='persistence test' 実際='$RESULT'"
  exit 1
fi

echo ""
echo "=== 0-5. HTTPS 公開検証 ==="

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
kubectl exec -n infrabox-vms vm-persistent -c vm -- \
  bash -c "nohup python3 -m http.server 8000 > /tmp/http.log 2>&1 &"

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
HTTP_STATUS=$(curl -sk -o /dev/null -w '%{http_code}' \
  --resolve "${HOSTNAME}:8443:127.0.0.1" "https://${HOSTNAME}:8443/")

echo ""
if [ "$HTTP_STATUS" = "200" ]; then
  echo "============================================"
  echo " HTTPS でアクセスできました"
  echo "    URL: https://$HOSTNAME:8443/ (port-forward経由)"
  echo "============================================"
else
  echo "============================================"
  echo " HTTPS 失敗: HTTP Status $HTTP_STATUS"
  echo "============================================"
  echo ""
  echo " デバッグ:"
  echo "   kubectl get certificate -n infrabox-vms"
  echo "   kubectl describe ingress vm-persistent-ingress -n infrabox-vms"
  exit 1
fi

echo ""
echo "============================================"
echo " Mac ローカル検証 完了！"
echo "============================================"
echo ""
echo " チェック1: exec 接続OK"
echo " チェック2: PVC 永続化OK"
echo " チェック3: HTTPS 公開OK"
echo ""
echo " port-forward プロセス（バックグラウンド実行中）:"
echo "   nginx    : localhost:8443"
echo " 停止するには: pkill -f 'kubectl.*port-forward'"
echo ""
echo " クリーンアップするには:"
echo "   ./scripts/local-cleanup.sh"
echo ""
