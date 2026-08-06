#!/usr/bin/env bash
# M0127 S7-gate: prefix/condition bisect for TestPort_IsolationEvalPlanQual
# (AI-20260806-011323-001). usage: run.sh <label> <file-with-test-names> [nightly]
# EvalPlanQual is appended to the -run set automatically; "nightly" reproduces
# ci/batch/stages/stage-testport.sh's cgroup + GOMEMLIMIT env exactly.
set -uo pipefail
lab="$1"; list="$2"; mode="${3:-plain}"
out="analysis/m0127-epq-bisect/$lab.log"
names=$( (cat "$list"; echo TestPort_IsolationEvalPlanQual) | awk 'NF && !s[$0]++' | paste -sd'|' )
if [[ "$mode" == nightly ]]; then
  GOOPG_CG_UNIT="goopg-epqbisect-$lab" GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
    scripts/goopg-test-run.sh go test -v -timeout 120m -run "^($names)$" ./internal/testport/ >"$out" 2>&1
else
  go test -v -timeout 120m -run "^($names)$" ./internal/testport/ >"$out" 2>&1
fi
n=$(grep -c . "$list")
if   grep -q '^--- FAIL: TestPort_IsolationEvalPlanQual ' "$out"; then v=EPQ_FAIL
elif grep -q '^--- PASS: TestPort_IsolationEvalPlanQual ' "$out"; then v=EPQ_PASS
else v=EPQ_ABSENT; fi
echo "$lab mode=$mode predecessors=$n -> $v"
grep '^--- FAIL' "$out" | grep -v EvalPlanQual | sed 's/^/    other-fail: /'
exit 0
