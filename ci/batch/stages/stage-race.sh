#!/bin/bash
# S1 Lane L step 2 — race detector, same EXCLUDE list as CI (ci/design/02 §A).
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

mkdir -p "${RUN_DIR}/race"
progress "S1.L" "race start (unit=goopg-nightly-race high=6G max=8G)"

# RACE_TIMEOUT raised for the nightly co-load (same rationale as stage-units;
# race tests run 2-3x slower to begin with).
rc=0
GOOPG_CG_UNIT=goopg-nightly-race GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
    "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    env "GOFLAGS=-p=${NIGHTLY_GO_P:-4}" \
    make -C "${REPO_ROOT}" race-gate RACE_TIMEOUT="${NIGHTLY_RACE_TIMEOUT:-45m}" \
    > "${RUN_DIR}/race/go-test.log" 2>&1 || rc=$?

stop_scope goopg-nightly-race
if [[ ${rc} -eq 0 ]]; then
    progress "S1.L" "race PASS"
    stage_status race "pass"
else
    progress "S1.L" "race FAIL (rc=${rc}) — see race/go-test.log"
    stage_status race "fail"
fi
exit ${rc}
