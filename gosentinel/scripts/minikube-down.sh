#!/usr/bin/env bash
# =============================================================================
# GoSentinel — Tear down Minikube deployment
# Usage: ./scripts/minikube-down.sh [--delete-cluster]
# =============================================================================
set -euo pipefail

DELETE_CLUSTER=false
while [[ $# -gt 0 ]]; do
  case $1 in
    --delete-cluster) DELETE_CLUSTER=true; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

NAMESPACE="gosentinel"

echo "Deleting GoSentinel namespace and all resources..."
kubectl delete namespace "$NAMESPACE" --ignore-not-found --timeout=60s

if [[ "$DELETE_CLUSTER" == "true" ]]; then
  echo "Deleting Minikube cluster..."
  minikube delete
  echo "Minikube cluster deleted."
else
  echo "Namespace deleted. Minikube cluster is still running."
  echo "To delete the cluster: ./scripts/minikube-down.sh --delete-cluster"
fi
