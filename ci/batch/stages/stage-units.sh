#!/bin/bash
# S1 Lane L step 1 — CI-parity unit/component suite (ci/design/02 §A).
#
# Runs the exact CI package set DIRECTLY (not via ralph-precommit-test.sh):
# the precommit tool hard-codes `-timeout 10m`, which is right for an
# uncontended pre-commit run but too tight for the nightly, where Lane H's
# pgbench (SF50 init + 100 clients) and the TPC-H data copy share the disk —
# the I/O-heavy internal/initdb package was observed blowing the 10m limit
# under exactly that co-load. NIGHTLY_UNITS_TIMEOUT defaults to 30m.
#
# Whole stage runs inside its own cgroup scope; env vars sit BEFORE the
# wrapper (goopg-test-run.sh reads GOOPG_CG_UNIT/GOOPG_MEM_* from its own env).
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

UNITS_TIMEOUT="${NIGHTLY_UNITS_TIMEOUT:-30m}"
# The CI EXCLUDE list (.github/workflows/test.yml / ralph-precommit-test.sh /
# Makefile RACE_EXCLUDE — all carry the same alternation).
EXCLUDE='internal/testport|internal/postmaster|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/'

mkdir -p "${RUN_DIR}/units"
progress "S1.L" "units start (unit=goopg-nightly-units high=6G max=8G p=${NIGHTLY_GO_P:-4} timeout=${UNITS_TIMEOUT})"

rc=0
( cd "${REPO_ROOT}" && \
  PKGS=$(go list ./... | grep -vE "${EXCLUDE}") && \
  GOOPG_CG_UNIT=goopg-nightly-units GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
  GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
      "${REPO_ROOT}/scripts/goopg-test-run.sh" \
      env "GOFLAGS=-p=${NIGHTLY_GO_P:-4}" \
      go test -timeout "${UNITS_TIMEOUT}" ${PKGS} \
) > "${RUN_DIR}/units/go-test.log" 2>&1 || rc=$?

stop_scope goopg-nightly-units
if [[ ${rc} -eq 0 ]]; then
    progress "S1.L" "units PASS"
    stage_status units "pass"
else
    progress "S1.L" "units FAIL (rc=${rc}) — see units/go-test.log"
    stage_status units "fail"
fi
exit ${rc}
