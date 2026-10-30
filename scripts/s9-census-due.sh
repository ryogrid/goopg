#!/usr/bin/env bash
# s9-census-due.sh — banner rule S9(1)/(4a): decide census freshness and
# compare a fresh capture mechanically (owner decision 2026-10-30; protected
# harness file — the loop never edits it).
#
#   scripts/s9-census-due.sh
#       Run bare from the repository root. First line is the verdict:
#         REUSE <artifact> base=<commit|tree>                         exit 0
#         DUE   <artifact|none> base=<…> reason=<why>                 exit 1
#       then one `BASE <corpus> <plans-file>` line per corpus (the newest
#       committed goopg capture of that corpus — what a fresh capture is
#       compared against) and, for DUE, up to 20 triggering paths.
#       Anything other than a first line starting with `REUSE `, or a
#       non-zero exit, means DUE.
#
#   scripts/s9-census-due.sh --compare <capture-dir>
#       For each corpus, compares the goopg plan text of the fresh capture in
#       <capture-dir> (`*-<corpus>.plans.txt`, not `*-pg.plans.txt`; `#`
#       header lines excluded) with its BASE file. <capture-dir> must be under
#       tmp/, hold exactly one capture per corpus, and each capture must be
#       newer than the commit that added its BASE file. Prints
#         <corpus> IDENTICAL|CHANGED|MISSING new=<sha256> base=<sha256> <base-file>
#       exit 0 only when every corpus is IDENTICAL.
#
#   scripts/s9-census-due.sh --rev <rev>
#       Owner replay at an older revision; its verdict line starts with
#       `REPLAY-` and is never a REUSE.
#
# Freshness base: the newest analysis/m0146/m0146-0014/routing-*/ROUTING.md
# (newest adding commit; within one commit the greatest directory name). Its
# first line is `# M0146-0014 — census at <sha> (<date>): <verdict>` (older
# records: `S9 re-check at <sha>`). If the record carries `tree=<40-hex>`
# (written only when the capture's binary was built from a working tree
# equal to the index — banner S9(4a)), the base is that tree; otherwise the
# capture commit, which must be an ancestor of HEAD.
#
# DUE when `git diff <base> HEAD` (no renames) touches the trigger set — the
# inputs of the EXPLAIN plan text the census compares (EXPLAIN only; the
# TPC-H lane also runs a seeded ANALYZE):
#   planner + plan inputs (non-test Go): internal/optimizer, internal/parser,
#     internal/nodes, internal/catalog, internal/initdb (installs the
#     planner's relpages / relallvisible / endpoint hooks), internal/
#     postmaster/dispatch.go (GUC -> planner settings), internal/utils/misc
#     (GUC boot values), internal/utils/pgprng (GEQO RNG), internal/access/
#     nbtree, internal/storage/vm.go and bufpool.go (all-visible and block
#     counts), and in internal/executor: *explain*, *analyze*, *stat*,
#     fold_func.go and numeric.go (plan-time constant folding),
#     actual_variable_endpoint.go, plus any executor diff line mentioning
#     Explain/explain; anywhere under internal/: *cost*, *selfuncs*,
#     *relsize*, *plannersettings*;
#   census instruments: scripts/jointree-parity-capture.sh,
#     scripts/capture-tpcds.sh, scripts/tpch-estimate-audit-arm.sh,
#     scripts/pg-plan-parity-diff.py, scripts/lib/tpch-private-clone.sh,
#     scripts/lib/capture-stamp.sh, cmd/estimate-audit, internal/testutil/
#     tpch, internal/testutil/estimateaudit, bench/tpcds/env_tpcds.sh;
#   this script itself — CORPUS_EPOCH below is bumped by the owner on a
#     corpus reload or rebuild, which forces DUE;
#   corpus reload / rebuild records: bench/tpcds/README.md,
#     bench/tpch/spotcheck_expected.env, bench/tpch/baseline-digests.txt,
#     ci/batch/tpch-row-anchors.csv,
#     bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt.
# Backstop: DUE when the capture is older than BACKSTOP_HOURS and any other
# non-test Go under internal/ or cmd/ changed since.
set -uo pipefail

BACKSTOP_HOURS=24          # owner constant — no environment override
CORPUS_EPOCH=2026-10-20    # owner: bump on a TPC-H/TPC-DS corpus reload or rebuild (forces DUE)
ROUTING_GLOB='analysis/m0146/m0146-0014/routing-*/ROUTING.md'
CORPORA=(tpch tpcds-sf025 tpcds-sf1)

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "DUE   none base=? reason=not-a-git-repo"; exit 1; }
cd "$ROOT" || exit 1

REV=HEAD MODE=check CMPDIR="" PFX=""
case "${1:-}" in
    "") ;;
    --rev) [[ -n "${2:-}" ]] || { echo "DUE   none base=? reason=usage"; exit 1; }; REV=$2; PFX="REPLAY-" ;;
    --compare) [[ -d "${2:-}" ]] || { echo "usage: $0 --compare <capture-dir>"; exit 2; }; MODE=compare; CMPDIR=$2 ;;
    *) echo "DUE   none base=? reason=usage (run bare)"; exit 1 ;;
esac

# --- newest artifact ----------------------------------------------------------
art=""
newest_commit=$(git log -1 --format=%H --diff-filter=A "$REV" -- "$ROUTING_GLOB" 2>/dev/null)
if [[ -n "$newest_commit" ]]; then
    art=$(git show --format= --name-only --diff-filter=A "$newest_commit" -- "$ROUTING_GLOB" | sort -V | tail -1)
    art="${art%/ROUTING.md}"
fi

# --- per-corpus BASE: newest committed goopg capture of each corpus -----------
declare -A BASEFILE=()
declare -A BASECT=()
for corpus in "${CORPORA[@]}"; do
    glob="analysis/m0146/m0146-0014/routing-*/*-${corpus}.plans.txt"
    f="" ct=0
    c=$(git log -1 --format=%H --diff-filter=A "$REV" -- "$glob" 2>/dev/null)
    if [[ -n "$c" ]]; then
        f=$(git show --format= --name-only --diff-filter=A "$c" -- "$glob" | sort -V | tail -1)
        ct=$(git log -1 --format=%ct "$c")
    fi
    BASEFILE[$corpus]="${f:-none}"
    BASECT[$corpus]=$ct
done
emit_base() { local c; for c in "${CORPORA[@]}"; do echo "BASE  $c ${BASEFILE[$c]}"; done; }

plan_sha() { # <rev:path | file> -> sha256 of the plan text without '#' lines
    if [[ -f "$1" ]]; then grep -v '^#' "$1"; else git show "$1" 2>/dev/null | grep -v '^#'; fi | sha256sum | cut -c1-64
}

if [[ "$MODE" == compare ]]; then
    real=$(realpath -e "$CMPDIR" 2>/dev/null)
    if [[ "$real" != "$ROOT/tmp/"* ]]; then
        echo "compare: <capture-dir> must be under $ROOT/tmp/"; exit 1
    fi
    rc=0
    for corpus in "${CORPORA[@]}"; do
        mapfile -t cands < <(find "$real" -maxdepth 1 -name "*-${corpus}.plans.txt" ! -name '*-pg.plans.txt')
        b="${BASEFILE[$corpus]}"
        if (( ${#cands[@]} != 1 )) || [[ "$b" == none ]]; then
            echo "$corpus MISSING captures=${#cands[@]} (need exactly 1) base=$b"; rc=1; continue
        fi
        new=${cands[0]}
        if (( $(stat -c %Y "$new") <= ${BASECT[$corpus]} )); then
            echo "$corpus STALE $new is not newer than its BASE commit"; rc=1; continue
        fi
        ns=$(plan_sha "$new"); bs=$(plan_sha "HEAD:$b")
        if [[ "$ns" == "$bs" ]]; then v=IDENTICAL; else v=CHANGED; rc=1; fi
        echo "$corpus $v new=$ns base=$bs $b"
    done
    exit $rc
fi

if [[ -z "$art" ]]; then
    echo "${PFX}DUE   none base=? reason=no-artifact"; emit_base; exit 1
fi
first=$(git show "$REV:$art/ROUTING.md" 2>/dev/null | head -1)
sha=$(grep -oE '(census|re-check) at `?[0-9a-f]{7,40}' <<<"$first" | head -1 | grep -oE '[0-9a-f]{7,40}$')
if [[ -z "$sha" ]] || ! full=$(git rev-parse --verify -q "$sha^{commit}"); then
    echo "${PFX}DUE   $art base=? reason=unparsable-capture-sha"; emit_base; exit 1
fi
tree=$(git show "$REV:$art/ROUTING.md" 2>/dev/null | grep -oE '(^|[[:space:]`])tree=[0-9a-f]{40}' | head -1 | grep -oE '[0-9a-f]{40}')
if [[ -n "$tree" ]] && git cat-file -e "$tree^{tree}" 2>/dev/null; then
    base=$tree
else
    if ! git merge-base --is-ancestor "$full" "$REV"; then
        echo "${PFX}DUE   $art base=$sha reason=capture-not-ancestor"; emit_base; exit 1
    fi
    base=$full
fi

TRIGGERS=(
    'internal/optimizer/' 'internal/parser/' 'internal/nodes/' 'internal/catalog/' 'internal/initdb/'
    'internal/postmaster/dispatch.go' 'internal/utils/misc/' 'internal/utils/pgprng/'
    'internal/access/nbtree/' 'internal/storage/vm.go' 'internal/storage/bufpool.go'
    ':(glob)internal/executor/*explain*' ':(glob)internal/executor/*analyze*' ':(glob)internal/executor/*stat*'
    'internal/executor/fold_func.go' 'internal/executor/numeric.go' 'internal/executor/actual_variable_endpoint.go'
    ':(glob)internal/**/*cost*' ':(glob)internal/**/*selfuncs*' ':(glob)internal/**/*relsize*'
    ':(glob)internal/**/*plannersettings*'
    'scripts/jointree-parity-capture.sh' 'scripts/capture-tpcds.sh' 'scripts/tpch-estimate-audit-arm.sh'
    'scripts/pg-plan-parity-diff.py' 'scripts/lib/tpch-private-clone.sh' 'scripts/lib/capture-stamp.sh'
    'cmd/estimate-audit/' 'internal/testutil/tpch/' 'internal/testutil/estimateaudit/' 'bench/tpcds/env_tpcds.sh'
    'bench/tpcds/README.md' 'bench/tpch/spotcheck_expected.env' 'bench/tpch/baseline-digests.txt'
    'ci/batch/tpch-row-anchors.csv' 'bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt'
    'scripts/s9-census-due.sh'
)
NOTEST=(':(exclude,glob)**/*_test.go')

git_error() { echo "${PFX}DUE   $art base=${base:0:12} reason=git-error($1)"; emit_base; exit 1; }
names=$(git diff --no-renames --name-only "$base" "$REV" -- "${TRIGGERS[@]}" "${NOTEST[@]}") || git_error trigger-diff
hits=()
[[ -n "$names" ]] && mapfile -t hits <<<"$names"
# An executor diff that edits EXPLAIN code outside the *explain* files.
exdiff=$(git diff --no-renames "$base" "$REV" -- 'internal/executor/' "${NOTEST[@]}") || git_error executor-diff
n=$(grep -cE '^[-+][^-+].*[Ee]xplain' <<<"$exdiff")
if [[ "${n:-0}" -gt 0 ]]; then hits+=("internal/executor (${n} diff lines mention Explain)"); fi

if (( ${#hits[@]} > 0 )); then
    echo "${PFX}DUE   $art base=${base:0:12} reason=plan-relevant-change"
    emit_base
    printf '  %s\n' "${hits[@]:0:20}"
    exit 1
fi
git diff --quiet --no-renames "$base" "$REV" -- 'internal/' 'cmd/' "${NOTEST[@]}" ':(exclude,glob)**/*.md'
other=$?
(( other > 1 )) && git_error backstop-diff
if (( other == 1 )); then
    if [[ "$REV" == HEAD ]]; then now=$(date +%s); else now=$(git log -1 --format=%ct "$REV"); fi
    cap_ts=$(git log -1 --format=%ct "$full")
    if (( now - cap_ts > BACKSTOP_HOURS * 3600 )); then
        echo "${PFX}DUE   $art base=${base:0:12} reason=backstop(${BACKSTOP_HOURS}h, other code changed since)"
        emit_base
        exit 1
    fi
fi
if [[ -n "$PFX" ]]; then
    echo "${PFX}REUSE $art base=${base:0:12}"; emit_base; exit 1
fi
echo "REUSE $art base=${base:0:12}"
emit_base
exit 0
