#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# InfraBox ローカル検証 — クリーンアップ
# 使い方:
#   ./scripts/local-cleanup.sh
#
# Lima VM ごと削除して完全にリセットする
# ============================================================

export KUBECONFIG=~/.kube/config-infrabox

echo "=== クリーンアップ開始 ==="

# --- K8s リソース削除（VM が動いている場合のみ） ---
if kubectl cluster-info &>/dev/null 2>&1; then
  echo ""
  echo "--- Ingress / Service 削除 ---"
  kubectl delete ingress vm-persistent-ingress -n infrabox-vms --ignore-not-found
  kubectl delete svc vm-persistent-http-svc -n infrabox-vms --ignore-not-found

  echo "--- Pipe CRD 削除 ---"
  kubectl delete pipe vm-persistent -n infrabox --ignore-not-found
  kubectl delete pipe vm-test -n infrabox --ignore-not-found

  echo "--- sshpiper アンインストール ---"
  helm uninstall sshpiper --namespace infrabox 2>/dev/null || true

  echo "--- Host Key Secret 削除 ---"
  kubectl delete secret infrabox-host-key -n infrabox --ignore-not-found

  echo "--- テスト用 Pod・Service・PVC 削除 ---"
  kubectl delete pod vm-persistent -n infrabox-vms --ignore-not-found
  kubectl delete svc vm-persistent-svc -n infrabox-vms --ignore-not-found
  kubectl delete pvc pvc-test -n infrabox-vms --ignore-not-found
  kubectl delete pod vm-test -n infrabox-vms --ignore-not-found
  kubectl delete svc vm-test-svc -n infrabox-vms --ignore-not-found

  echo "--- ClusterIssuer 削除 ---"
  kubectl delete clusterissuer selfsigned --ignore-not-found

  echo "--- Helm チャート削除 ---"
  helm uninstall ingress-nginx --namespace ingress-nginx 2>/dev/null || true
  helm uninstall cert-manager --namespace cert-manager 2>/dev/null || true

  echo "--- Namespace 削除 ---"
  kubectl delete namespace infrabox-vms --ignore-not-found
  kubectl delete namespace infrabox --ignore-not-found
  kubectl delete namespace ingress-nginx --ignore-not-found
  kubectl delete namespace cert-manager --ignore-not-found
else
  echo "  K8s クラスターに接続できません。リソース削除をスキップ。"
fi

echo ""
echo "--- Lima VM 削除 ---"
if limactl list -q 2>/dev/null | grep -q '^k3s$'; then
  limactl stop k3s 2>/dev/null || true
  limactl delete k3s
  echo "  Lima VM 'k3s' を削除しました"
else
  echo "  Lima VM 'k3s' は存在しません。スキップ。"
fi

echo ""
echo "--- kubeconfig 削除 ---"
rm -f ~/.kube/config-infrabox
echo "  ~/.kube/config-infrabox を削除しました"

echo ""
echo "--- ローカル Docker イメージ削除 ---"
docker rmi infrabox-base:ubuntu-24.04 2>/dev/null || true

echo ""
echo "============================================"
echo " ✅ クリーンアップ完了"
echo "============================================"
echo ""
echo " 削除されたもの:"
echo "   - K8s リソース（Pod, Service, PVC, Ingress, Pipe, Secret）"
echo "   - Helm リリース（sshpiper, ingress-nginx, cert-manager）"
echo "   - Namespace（infrabox, infrabox-vms, ingress-nginx, cert-manager）"
echo "   - Lima VM 'k3s'"
echo "   - kubeconfig (~/.kube/config-infrabox)"
echo "   - Docker イメージ (infrabox-base:ubuntu-24.04)"
echo ""
