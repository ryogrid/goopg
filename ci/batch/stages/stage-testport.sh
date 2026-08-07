#!/bin/bash
# S1 Lane H step 1 — the oracle-port suite: the WHOLE internal/testport
# package with NO -run filter (ci/design/02 §A: 4 of the 60 must-pass rows are
# TestE2E_Failover*, which a TestPort_-only filter would silently skip).
# Covers the 60 port/yes rows + regress (232 subtests) + isolation (121).
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

mkdir -p "${RUN_DIR}/testport"
REGRESS_DIFF_DIR="${RUN_DIR}/testport/regress-diffs"
mkdir -p "${REGRESS_DIFF_DIR}"
progress "S1.H" "testport start (unit=goopg-nightly-testport high=6G max=8G timeout=120m)"

rc=0
( cd "${REPO_ROOT}" && \
  GOOPG_REGRESS_DIFF_DIR="${REGRESS_DIFF_DIR}" \
  GOOPG_CG_UNIT=goopg-nightly-testport GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
      "${REPO_ROOT}/scripts/goopg-test-run.sh" \
      go test -v -timeout 120m ./internal/testport/ \
) > "${RUN_DIR}/testport/go-test.log" 2>&1 || rc=$?

stop_scope goopg-nightly-testport

# NOTE: the former GOOPG_WAL_CANONICAL=on "canonical resume lane" was removed
# when canonical WAL emission + the knob were deleted (docs/design/
# wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md). The
# real-PG-consumer tests it re-ran (TestE2E_FailoverGoopgToPG,
# TestE2E_ChecksumStreamingGoopgToPG, TestPort_PgWaldump002SaveFullpage,
# TestPort_PgWaldumpVacuumPruneRoundtrip, TestPort_WALPgWaldumpCompat) now
# unconditionally t.Skip (see .ralph/deferral_ledger.md) until the native->PG
# record-body content rewrite lands, so there is nothing to re-run.
if [[ ${rc} -eq 0 ]]; then
    progress "S1.H" "testport PASS"
    stage_status testport "pass"
else
    progress "S1.H" "testport FAIL (rc=${rc}) — see testport/go-test.log"
    stage_status testport "fail"
fi
exit ${rc}
