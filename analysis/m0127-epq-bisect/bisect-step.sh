#!/usr/bin/env bash
# `git bisect run` step for TestPort_IsolationEvalPlanQual (AI-20260806-011323-001).
# Terms: old=broken (EPQ fails), new=fixed (EPQ passes).
# exit 0 -> broken (old);  exit 1 -> fixed (new);  exit 125 -> skip (build break).
WT=/home/ryo/work/goopg/wt-epq
LOGDIR=/home/ryo/work/goopg/goopg/analysis/m0127-epq-bisect
cd "$WT" || exit 125
# the PG oracle tree is an untracked symlink; recreate it after every checkout
[[ -L postgres ]] || { rm -rf postgres; ln -s /home/ryo/work/goopg/goopg/postgres postgres; }
sha=$(git rev-parse --short HEAD)
go build ./... >/dev/null 2>&1 || exit 125
go test -v -timeout 30m -run '^TestPort_IsolationEvalPlanQual$' ./internal/testport/ \
    > "$LOGDIR/bisect-$sha.log" 2>&1
if grep -q '^--- FAIL: TestPort_IsolationEvalPlanQual ' "$LOGDIR/bisect-$sha.log"; then
    echo "$sha BROKEN"; exit 0
elif grep -q '^--- PASS: TestPort_IsolationEvalPlanQual ' "$LOGDIR/bisect-$sha.log"; then
    echo "$sha FIXED"; exit 1
fi
echo "$sha INDETERMINATE"; exit 125
