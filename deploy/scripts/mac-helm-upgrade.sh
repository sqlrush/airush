#!/bin/zsh
# 只做 helm upgrade（不重建镜像），用于 chart 模板改动的快速验证。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush
helm upgrade --install airush deploy/charts/airush --kube-context kind-airush-dev \
  -f deploy/charts/airush/values-dev.yaml --wait --timeout 5m
kubectl --context kind-airush-dev get pods
