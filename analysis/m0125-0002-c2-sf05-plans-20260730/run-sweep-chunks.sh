#!/usr/bin/env bash
# M0125-0002 commit 2 — the SF0.5 ANSWER gate (D4 item 4).
#
# WHY THIS RUNS EVEN THOUGH EVERY PLAN IS IDENTICAL
#
# capture-plans.sh found 96/96 SF0.5 plans and 22/22 TPC-H plans byte-identical
# between HEAD and commit 2, which settles the question the design was actually
# worried about (D2 row 2: "it does move plans"). It does not settle everything.
#
# goopg's EXPLAIN prints predicates by column NAME. The conversion changed one
# thing that no plan text can show: the old hand-written arms REBUILT BinaryOp,
# UnaryOp and FuncCall from a field list instead of copying the struct, and the
# field lists were stale — BinaryOp.ResultType ("non-empty for arithmetic with
# typed result", e.g. int2), FuncCall.Variadic and FuncCall.ReturnType were
# dropped on every hoisted conjunct. shallowCloneExpr copies the whole struct,
# so those fields now survive. That is a fix, and it is a fix the executor can
# act on (ResultType selects arithmetic result typing) while EXPLAIN renders
# both versions identically.
#
# So the plan gate proves no conjunct changed PLACE; only the answer gate can
# prove none changed VALUE. Per CLAUDE.md hard-won rule #1, a silent row-count
# or checksum change is this project's most expensive failure mode, and value
# checksums (M0124-0005) are what make this sweep able to see it at all.
#
# Baseline: analysis/m0125-0003-sf05-relsize-20260730/sweep-COMPLETE-20260730-155432.txt
#   (PASS=82 TIMEOUT=13 MISMATCH=0 CKMISMATCH=0 ERROR=0 SKIP=4). That arm ran
#   with GOOPG_RELSIZE_FALLBACK=2 explicitly; since M0125-0005 flipped the
#   default, UNSET is that same stage, so no flag is exported here and the
#   report's `# planner-flags:` line records which regime was measured.
#
# Chunked for the same reason the two previous SF0.5 artefacts were: each chunk
# starts a fresh server and runs S-cold, which is the gate's own determinism
# contract, and ~2 h exceeds one foreground call.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
OUT="analysis/m0125-0002-c2-sf05-plans-20260730"
# Private binary: tmp/goopg-bench-bin is shared with the nightly lane.
export GOOPG_BIN="$PWD/tmp/goopg-c2-new"
export SF05_NO_BUILD=1   # reuse the exact binary capture-plans.sh measured
export SF05_RESULTS_DIR="$OUT"
export TIMEOUT_SEC=300 RESTART_AFTER_TIMEOUT=1

range="${1:?usage: run-sweep-chunks.sh \"<first> <last>\"}"
set -- $range
echo "===== CHUNK Q$1-Q$2 begins $(date -Iseconds) ====="
QUERIES="$(seq "$1" "$2")" scripts/tpcds-sf05-regression.sh sweep
echo "===== CHUNK Q$1-Q$2 rc=$? ends $(date -Iseconds) ====="
