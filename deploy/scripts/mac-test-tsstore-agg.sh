#!/bin/zsh
# 只跑 spec-1.5 D6 新增的存储引擎行为用例（T13/T15/T16/T17/T19/T20）。
# 与 mac-test-tsstore.sh 分开：那个脚本 tail -40，新增用例排在前面会被截掉，
# 看不到就等于没验。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
go test -tags=integration -count=1 -v -run \
  'TestBatchSplitting|TestContinuousAggregateConsistency|TestLayerSelectionSurvivesRetention|TestDatasourceDeleteCascade' \
  ./internal/tsstore/ 2>&1 | grep -vE "^20[0-9]{2}/|🐳|✅|⏳|🔔|🚫|Shell not found"
