#!/usr/bin/env bash
# lever-count.sh — the M0147 Movement instrument (owner decision 2026-10-21).
#
# Counts the distinct "GOOPG_*" environment-lever literals in non-test Go code
# under internal/ and cmd/, excluding the provenance registry
# (internal/optimizer/flaglabels.go keeps retired rows on purpose) and the
# KEEP list below — levers M0147 deliberately retains (measurement seeds,
# diagnostic traces, owner-decided escape hatches, test-harness and profiling
# switches, safety assertions). Every M0147 slice that retires a lever closes
# with `Movement: yes — lever-count N→N-k (census … held)`.
#
# Usage: scripts/lever-count.sh [--list]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# KEEP (docs/milestones/0147-lever-retirement.md §Keep). Extended regex, anchored.
KEEP='^GOOPG_(ANALYZE_SEED|ANALYZE_SEED_CHILD|PGSHAPED_DP_TRACE|NLI_CENSUS|GEQO_TRACE|GEQO_TRACE_EVAL|NLI_COSTGATE|NLI_COSTGATE_DEBUG|WAL_ALLOW_EARLY_END|UPDATE_GOLDENS|SKIP_[A-Z0-9_]+|RUN_BLOCKED_[A-Z0-9_]+|SKIP_PGNODES_ORACLE|TPCH_QUERIES|REGRESS_WEDGE_DIR|REGRESS_DIFF_DIR|PPROF_ADDR|TEST_[A-Z0-9_]+|SEGV_BACKTRACE_SO|MUTEX_PROFILE_RATE|BLOCK_PROFILE_RATE|LOG_STATEMENT|IO_TRACE|DEBUG_WFG|BTREE_PARSE_ERR_DUMP|ASSERT_ROW_SHAPE|PDLSN_ASSERT)$'

levers=$(grep -rhoE '"GOOPG_[A-Z0-9_]+"' --include='*.go' internal cmd \
           --exclude='*_test.go' --exclude='flaglabels.go' \
         | tr -d '"' | sort -u | grep -vE "${KEEP}" || true)

if [[ "${1:-}" == "--list" ]]; then
  printf '%s\n' "${levers}" | sed '/^$/d'
fi
echo "lever-count $(printf '%s\n' "${levers}" | sed '/^$/d' | wc -l)"
