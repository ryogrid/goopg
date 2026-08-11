#!/usr/bin/env bash
#
# capture-ev-action.sh — capture the on-disk artefacts for a nailed system view
# from a throwaway PostgreSQL 18.3 oracle.
#
# M0131-S7 (docs/design/0131-0007-ev-action-capture-tooling.md).
#
# Each nailed system view goopg hosts costs three artefacts that used to be
# hand-transcribed: the verbatim `pg_rewrite.ev_action` pg_node_tree blob
# (internal/initdb/<view>_ev_action.dat), the `nailedAttr` table, and the
# `nailedRel` row. system_views.sql holds 80 views; transcribing the rest by
# hand is not a plan. This script owns the only non-reproducible step — running
# a real PG — and emits the blobs plus a TSV manifest that
# cmd/gen-nailed-view-tables (M0131-S7.4) renders into Go.
#
# Under the M0131-S8a Option-A policy goopg PINS its system-view OIDs to
# upstream's own initdb assignments, so the oracle→goopg OID mapping is the
# IDENTITY function: nothing inside a captured blob is ever rewritten. This
# script therefore asserts identity rather than applying a mapping, and the
# acceptance test (--verify) is a plain byte `cmp` against the committed .dat
# files. Do NOT grow a rewriting pass here — see 0131-0008 for why.
#
# Usage:
#   scripts/capture-ev-action.sh [--out-dir DIR] [--manifest FILE] <view> [view ...]
#   scripts/capture-ev-action.sh --verify       # re-derive the committed six
#
#   --out-dir DIR    where <view>_ev_action.dat files are written
#                    (default: internal/initdb)
#   --manifest FILE  where the TSV manifest is written
#                    (default: internal/initdb/nailed_view_manifest.tsv)
#   --verify         capture the six pinned views into a scratch dir and
#                    require byte-identical agreement with the committed
#                    blobs and manifest. Emits nothing into the tree.
#   --keep           do not delete the throwaway cluster (debugging)
#
# Exit codes:
#   0  success (or, under --verify, everything byte-identical)
#   1  a guard failed (blob drift, unmapped relid, unknown atttypid, …)
#   2  operational failure (missing binary, initdb/psql failure, …)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PG_BIN="${REPO_ROOT}/postgres/local_install/bin"
PG_LIB="${REPO_ROOT}/postgres/local_install/lib"

export PATH="${PG_BIN}:${PATH}"
export LD_LIBRARY_PATH="${PG_LIB}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

INITDB_DIR="${REPO_ROOT}/internal/initdb"
OUT_DIR="${INITDB_DIR}"
MANIFEST="${INITDB_DIR}/nailed_view_manifest.tsv"
VERIFY=0
KEEP=0
VIEWS=()

# The initdb-assigned OID band (postgres/src/include/access/transam.h:194-196),
# mirrored by firstUnpinnedObjectID/firstNormalObjectID in
# internal/initdb/system_view_oid_pins.go.
BAND_LO=12000
BAND_HI=16384

die()  { echo "capture-ev-action: $*" >&2; exit 2; }
fail() { echo "capture-ev-action: GUARD FAILED: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out-dir)  OUT_DIR="$2"; shift 2 ;;
        --manifest) MANIFEST="$2"; shift 2 ;;
        --verify)   VERIFY=1; shift ;;
        --keep)     KEEP=1; shift ;;
        -h|--help)  sed -n '3,40p' "${BASH_SOURCE[0]}"; exit 0 ;;
        -*)         die "unknown option: $1" ;;
        *)          VIEWS+=("$1"); shift ;;
    esac
done

for b in initdb pg_ctl psql; do
    command -v "$b" >/dev/null 2>&1 || die "missing ${b} — expected under ${PG_BIN}"
done

# -------------------------------------------------------------------------- #
# Single sources of truth read out of the Go tree
# -------------------------------------------------------------------------- #
PINS_GO="${INITDB_DIR}/system_view_oid_pins.go"
TYPES_GO="${INITDB_DIR}/pg_type_seed_data.go"
[[ -f "$PINS_GO" ]]  || die "missing ${PINS_GO}"
[[ -f "$TYPES_GO" ]] || die "missing ${TYPES_GO}"

# pinned_view_oid[name] / pinned_rule_oid[name]: the M0131-S8a table. Parsed
# from the Go literal so the script cannot drift from the tree's own policy.
declare -A pinned_view_oid pinned_rule_oid pinned_reltype pinned_natts
declare -A pinned_oid_owner        # every pinned OID (view + rule) -> label
PINNED_ORDER=()
while IFS=$'\t' read -r name voi roi rt na; do
    pinned_view_oid["$name"]="$voi"
    pinned_rule_oid["$name"]="$roi"
    pinned_reltype["$name"]="$rt"
    pinned_natts["$name"]="$na"
    pinned_oid_owner["$voi"]="$name"
    pinned_oid_owner["$roi"]="${name}._RETURN"
    PINNED_ORDER+=("$name")
done < <(sed -n 's/^[[:space:]]*{"\([a-z_]*\)", *\([0-9]*\), *\([0-9]*\), *\([0-9]*\), *\([0-9]*\)},$/\1\t\2\t\3\t\4\t\5/p' "$PINS_GO")
[[ ${#PINNED_ORDER[@]} -gt 0 ]] || die "parsed zero pins out of ${PINS_GO}"

# known_type[oid]=1 for every type in the pg_type bootstrap set. attalign and
# attbyval are DERIVED from this set by bootstrapPgAttributeTuples, so a
# captured atttypid outside it has no resolvable alignment (guard #5).
declare -A known_type
while read -r oid; do known_type["$oid"]=1; done \
    < <(grep -o '{OID: [0-9]*' "$TYPES_GO" | grep -o '[0-9]*')
[[ ${#known_type[@]} -gt 0 ]] || die "parsed zero pg_type entries out of ${TYPES_GO}"

if [[ $VERIFY -eq 1 ]]; then
    [[ ${#VIEWS[@]} -eq 0 ]] || die "--verify takes no view arguments"
    VIEWS=("${PINNED_ORDER[@]}")
fi
[[ ${#VIEWS[@]} -gt 0 ]] || die "no views named (try --verify)"

# -------------------------------------------------------------------------- #
# Throwaway oracle cluster
# -------------------------------------------------------------------------- #
SCRATCH="${REPO_ROOT}/tmp/ev-action-capture"
PGDATA_DIR="${SCRATCH}/pgdata"
SOCK_DIR="${SCRATCH}/sock"
PG_LOG="${SCRATCH}/pg.log"
STAGE_DIR="${SCRATCH}/stage"

cleanup() {
    if [[ -d "$PGDATA_DIR" ]]; then
        pg_ctl -D "$PGDATA_DIR" -m immediate -w -t 20 stop >/dev/null 2>&1 || true
    fi
    [[ $KEEP -eq 1 ]] && return 0
    rm -rf "$SCRATCH"
}
trap cleanup EXIT INT TERM

rm -rf "$SCRATCH"
mkdir -p "$SOCK_DIR" "$STAGE_DIR"

echo "capture-ev-action: initdb'ing a throwaway PG 18.3 oracle in ${PGDATA_DIR}..."
# NB: PG 18's initdb has no -q/--quiet (only -d/--debug); silence it by
# redirecting rather than by a flag.
initdb -D "$PGDATA_DIR" --no-sync >/dev/null || die "initdb failed"
pg_ctl -D "$PGDATA_DIR" -l "$PG_LOG" -w -t 60 \
    -o "-k ${SOCK_DIR} -h '' -c listen_addresses=''" start >/dev/null \
    || { echo "--- ${PG_LOG} ---" >&2; cat "$PG_LOG" >&2 || true; die "pg_ctl start failed"; }

# -F $'\t' makes psql emit tab-separated columns, so no query has to
# concatenate them with || (which is ambiguous for "char" columns such as
# pg_class.relkind: "operator is not unique: text || \"char\"").
psql_q() { psql -X -A -t -q -F $'\t' -h "$SOCK_DIR" -d postgres -v ON_ERROR_STOP=1 -c "$1"; }

ORACLE_VERSION="$(psql_q "SELECT current_setting('server_version')")"
ORACLE_CATVERSION="$(pg_controldata -D "$PGDATA_DIR" | sed -n 's/^Catalog version number:[[:space:]]*//p')"

# -------------------------------------------------------------------------- #
# Capture
# -------------------------------------------------------------------------- #
MANIFEST_STAGE="${STAGE_DIR}/nailed_view_manifest.tsv"
{
    echo "# Code generated by scripts/capture-ev-action.sh; DO NOT EDIT."
    echo "# Oracle: PostgreSQL ${ORACLE_VERSION}, catalog version ${ORACLE_CATVERSION}."
    echo "# Captured from a throwaway 'initdb --no-sync' cluster (M0131-S7)."
    echo "#"
    echo "# rel  <name> <oracle_oid> <goopg_oid> <rule_oid> <oracle_reltype> <goopg_reltype> <relkind> <relnatts>"
    echo "# attr <name> <attnum> <attname> <atttypid> <attlen> <attnotnull> <attisdropped>"
} > "$MANIFEST_STAGE"

# goopg pins every nailed system view's reltype to 2249 (RECORDOID) where
# upstream mints a per-view composite pg_type row. That divergence is
# deliberate and is M0131-S6.5's open probe; the manifest records BOTH so the
# generator emits goopg's value and a guard can assert this stays the only
# divergence. See relcache_init.go:679-682 and system_view_oid_pins.go.
GOOPG_RELTYPE=2249

for view in "${VIEWS[@]}"; do
    echo "capture-ev-action: capturing pg_catalog.${view}..."

    # --- Query 3: the pg_class row (feeds nailedRel) --------------------- #
    rel_row="$(psql_q "SELECT c.oid, c.reltype, c.relkind, c.relnatts
                         FROM pg_class c WHERE c.oid = 'pg_catalog.${view}'::regclass")" \
        || die "pg_class query failed for ${view}"
    IFS=$'\t' read -r rel_oid rel_type rel_kind rel_natts <<< "$rel_row"
    [[ -n "$rel_oid" ]] || die "pg_catalog.${view} does not exist in the oracle"
    [[ "$rel_kind" == "v" ]] || fail "${view}: relkind is '${rel_kind}', not 'v'"

    rule_oid="$(psql_q "SELECT r.oid FROM pg_rewrite r
                         WHERE r.ev_class = 'pg_catalog.${view}'::regclass
                           AND r.rulename = '_RETURN'")"
    [[ -n "$rule_oid" ]] || fail "${view}: no _RETURN rule in pg_rewrite"

    # Guard: the OID mapping is the identity function (Option A). A pinned
    # view whose oracle OID moved means upstream's initdb assignment changed —
    # re-pin system_view_oid_pins.go deliberately, do not silently remap.
    goopg_oid="${pinned_view_oid[$view]:-}"
    if [[ -n "$goopg_oid" ]]; then
        [[ "$goopg_oid" == "$rel_oid" ]] \
            || fail "${view}: oracle OID ${rel_oid} != pinned goopg OID ${goopg_oid} (re-pin system_view_oid_pins.go)"
        [[ "${pinned_rule_oid[$view]}" == "$rule_oid" ]] \
            || fail "${view}: oracle rule OID ${rule_oid} != pinned ${pinned_rule_oid[$view]}"
        [[ "${pinned_reltype[$view]}" == "$rel_type" ]] \
            || fail "${view}: oracle reltype ${rel_type} != pinned UpstreamRelType ${pinned_reltype[$view]}"
        [[ "${pinned_natts[$view]}" == "$rel_natts" ]] \
            || fail "${view}: oracle relnatts ${rel_natts} != pinned ${pinned_natts[$view]}"
    else
        # A view with no pin cannot be emitted: its OID (and any dependent
        # view's embedded :relid) would be unowned. M0131-S8a's table is the
        # gate, exactly as the design requires.
        fail "${view}: no row in systemViewOIDPins() — add the pin first (M0131-S8a)"
    fi

    # --- Query 1: the ev_action blob ------------------------------------ #
    # The committed blobs are ONE line with NO trailing newline (first byte
    # '(', last byte ')'). psql appends exactly one newline; strip exactly one
    # byte and verify it was that newline, or every capture differs from the
    # committed bytes by one.
    raw="${STAGE_DIR}/${view}_ev_action.raw"
    dat="${STAGE_DIR}/${view}_ev_action.dat"
    psql -X -A -t -q -h "$SOCK_DIR" -d postgres -v ON_ERROR_STOP=1 \
        -c "SELECT r.ev_action FROM pg_rewrite r
             WHERE r.ev_class = 'pg_catalog.${view}'::regclass
               AND r.rulename = '_RETURN'" > "$raw" \
        || die "ev_action query failed for ${view}"
    [[ -s "$raw" ]] || fail "${view}: empty ev_action"
    [[ "$(tail -c1 "$raw" | od -An -tx1 | tr -d ' ')" == "0a" ]] \
        || fail "${view}: psql output did not end in a newline"
    head -c -1 "$raw" > "$dat"
    [[ "$(wc -l < "$dat")" -eq 0 ]] \
        || fail "${view}: ev_action spans multiple lines — the blob is not one line"
    [[ "$(head -c1 "$dat")" == "(" && "$(tail -c1 "$dat")" == ")" ]] \
        || fail "${view}: ev_action is not parenthesis-delimited"

    # Guard #4: every in-band :relid inside the blob must be an OID this tree
    # pins. Out-of-band relids are upstream-pinned catalogs (1259, 6100, …)
    # and need no mapping; an unpinned in-band relid names a view goopg has
    # not adopted, and embedding it would point a hosted PG at nothing.
    while read -r relid; do
        if [[ "$relid" -ge $BAND_LO && "$relid" -lt $BAND_HI ]]; then
            [[ -n "${pinned_oid_owner[$relid]:-}" ]] \
                || fail "${view}: blob embeds unpinned in-band :relid ${relid} — pin it in system_view_oid_pins.go first"
        fi
    done < <(grep -o ':relid [0-9]*' "$dat" | grep -o '[0-9]*' | sort -u)

    printf 'rel\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$view" "$rel_oid" "$goopg_oid" "$rule_oid" \
        "$rel_type" "$GOOPG_RELTYPE" "$rel_kind" "$rel_natts" >> "$MANIFEST_STAGE"

    # --- Query 2: the pg_attribute rows (feed nailedAttr) --------------- #
    # atttypid MUST come from the oracle, not from reading system_views.sql:
    # it is the type PG's parse analyzer assigned to the matching TargetEntry
    # INSIDE ev_action. A transcribed disagreement makes a hosted PG build a
    # TupleDesc that does not match the tuples the rule's plan produces
    # (tupdesc.c:105 elog(ERROR)); capturing both from one cluster makes the
    # agreement structural.
    attr_count=0
    while IFS=$'\t' read -r attnum attname atttypid attlen attnotnull attisdropped; do
        [[ -n "$attnum" ]] || continue
        [[ -n "${known_type[$atttypid]:-}" ]] \
            || fail "${view}.${attname}: atttypid ${atttypid} is absent from the pg_type bootstrap set — attalign/attbyval cannot be derived (add it to pg_type_seed_data.go first)"
        printf 'attr\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
            "$view" "$attnum" "$attname" "$atttypid" "$attlen" "$attnotnull" "$attisdropped" \
            >> "$MANIFEST_STAGE"
        attr_count=$((attr_count + 1))
    done < <(psql_q "SELECT a.attnum, a.attname, a.atttypid,
                            a.attlen, a.attnotnull, a.attisdropped
                       FROM pg_attribute a
                      WHERE a.attrelid = 'pg_catalog.${view}'::regclass AND a.attnum > 0
                      ORDER BY a.attnum")
    [[ "$attr_count" -eq "$rel_natts" ]] \
        || fail "${view}: captured ${attr_count} attributes but relnatts is ${rel_natts}"
done

# -------------------------------------------------------------------------- #
# Emit or verify
# -------------------------------------------------------------------------- #
rc=0
if [[ $VERIFY -eq 1 ]]; then
    echo "capture-ev-action: --verify: comparing against the committed tree..."
    for view in "${VIEWS[@]}"; do
        committed="${INITDB_DIR}/${view}_ev_action.dat"
        if [[ ! -f "$committed" ]]; then
            echo "  MISSING  ${committed}" >&2; rc=1; continue
        fi
        if cmp -s "${STAGE_DIR}/${view}_ev_action.dat" "$committed"; then
            echo "  ok       ${view}_ev_action.dat"
        else
            echo "  DRIFT    ${view}_ev_action.dat differs from the oracle" >&2
            cmp "${STAGE_DIR}/${view}_ev_action.dat" "$committed" >&2 || true
            rc=1
        fi
    done
    if [[ -f "$MANIFEST" ]]; then
        # Line 2 stamps the oracle build; compare the payload only.
        if diff <(grep -v '^#' "$MANIFEST_STAGE") <(grep -v '^#' "$MANIFEST") >/dev/null; then
            echo "  ok       $(basename "$MANIFEST")"
        else
            echo "  DRIFT    $(basename "$MANIFEST") differs from the oracle" >&2
            diff <(grep -v '^#' "$MANIFEST") <(grep -v '^#' "$MANIFEST_STAGE") >&2 || true
            rc=1
        fi
    else
        echo "  MISSING  ${MANIFEST}" >&2; rc=1
    fi
    [[ $rc -eq 0 ]] && echo "capture-ev-action: --verify PASS (${#VIEWS[@]} views byte-identical)"
    exit $rc
fi

mkdir -p "$OUT_DIR"
for view in "${VIEWS[@]}"; do
    cp "${STAGE_DIR}/${view}_ev_action.dat" "${OUT_DIR}/${view}_ev_action.dat"
    echo "capture-ev-action: wrote ${OUT_DIR}/${view}_ev_action.dat"
done
cp "$MANIFEST_STAGE" "$MANIFEST"
echo "capture-ev-action: wrote ${MANIFEST}"
echo "capture-ev-action: done (${#VIEWS[@]} views)"
