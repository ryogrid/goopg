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

# Inline heap-tuple budget for a seeded pg_rewrite row (guard #5).
# MaxHeapTupleSize = BLCKSZ - MAXALIGN(SizeOfPageHeaderData) -
# MAXALIGN(sizeof(ItemIdData)) = 8192 - 24 - 8 (postgres/src/include/access/
# htup_details.h:561-563). The other seven columns are fixed-width plus the
# "<>" ev_qual varlena and the tuple header, so reserve a flat 160 B.
MAX_HEAP_TUPLE_SIZE=8160
TUPLE_OVERHEAD_BUDGET=160
MAX_EV_ACTION_STORED=$((MAX_HEAP_TUPLE_SIZE - TUPLE_OVERHEAD_BUDGET))

# M0131-S20.2b: guard #5 is now "inline OR toastable", not "inline". The
# number above still names the same boundary as maxInlineEvActionStored
# (internal/initdb/pg_rewrite_toast_writer.go) — what changed is that crossing
# it is no longer fatal, because S20.2a taught goopg's initdb to externalise
# the value into base/{1,5}/2838. The two files below are the tree's own proof
# that the out-of-line path exists; if either goes away the guard reverts to
# the pre-S20.2 hard failure rather than silently seeding a value nothing can
# store.
TOAST_BOOTSTRAP_GO="${INITDB_DIR}/pg_rewrite_toast_bootstrap.go"
TOAST_WRITER_GO="${INITDB_DIR}/pg_rewrite_toast_writer.go"

die()  { echo "capture-ev-action: $*" >&2; exit 2; }
fail() { echo "capture-ev-action: GUARD FAILED: $*" >&2; exit 1; }

# -------------------------------------------------------------------------- #
# --prosqlbody mode (M0133-S2): capture the information_schema helper
# functions' pg_proc rows. The view corpus (above) captures pg_rewrite
# ev_action blobs; these 11 functions carry a SECOND node-tree surface — the
# new-style SQL-standard function body in pg_proc.prosqlbody (a pg_node_tree
# exactly like ev_action). 10 of the 11 helpers set prosrc='' and a non-null
# prosqlbody; only _pg_expandarray has a textual prosrc and prosqlbody=NULL.
# The `pgnodes` non-path argument applies unchanged: capture, do not generate.
# -------------------------------------------------------------------------- #
PROCSQLBODY=0
PROC_MANIFEST="${INITDB_DIR}/information_schema_proc_manifest.tsv"
PROC_OUT_DIR="${INITDB_DIR}"

# M0133-S4: the information_schema views share the capture pipeline but live in
# namespace 13273 and are pinned by informationSchemaViewOIDPins() (a separate
# Go table), so they need a separate manifest and a separate pin file. The
# flag selects the namespace the pg_class / pg_rewrite / pg_attribute queries
# resolve against and the pin file the script parses its guard data from.
INFO_SCHEMA=0
NSP_NAME="pg_catalog"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out-dir)  OUT_DIR="$2"; shift 2 ;;
        --manifest) MANIFEST="$2"; shift 2 ;;
        --verify)   VERIFY=1; shift ;;
        --keep)     KEEP=1; shift ;;
        --prosqlbody) PROCSQLBODY=1; shift ;;
        --information-schema) INFO_SCHEMA=1; shift ;;
        -h|--help)  sed -n '3,40p' "${BASH_SOURCE[0]}"; exit 0 ;;
        -*)         die "unknown option: $1" ;;
        *)          VIEWS+=("$1"); shift ;;
    esac
done

if [[ $INFO_SCHEMA -eq 1 ]]; then
    NSP_NAME="information_schema"
    PINS_GO="${INITDB_DIR}/information_schema_view_oid_pins.go"
    MANIFEST="${INITDB_DIR}/information_schema_view_manifest.tsv"
fi

for b in initdb pg_ctl psql; do
    command -v "$b" >/dev/null 2>&1 || die "missing ${b} — expected under ${PG_BIN}"
done

# -------------------------------------------------------------------------- #
# Single sources of truth read out of the Go tree
# -------------------------------------------------------------------------- #
# PINS_GO is the system_view_oid_pins.go default here; the --information-schema
# override above re-points it at information_schema_view_oid_pins.go.
if [[ $INFO_SCHEMA -eq 0 ]]; then
    PINS_GO="${INITDB_DIR}/system_view_oid_pins.go"
fi
# pgTypeCanonical, not pg_type_seed_data.go: the CANONICAL table is what
# bootstrapPgTypeTuples derives attalign/attbyval from, and it is a strict
# subset of the seed data. Parsing the wider file made guard #5 vacuous —
# M0131-S9.1's tranche needed 1007/1700/2211 added to pgTypeCanonical, which
# only the Go test caught.
TYPES_GO="${INITDB_DIR}/pg_type_bootstrap.go"
[[ -f "$PINS_GO" ]]  || die "missing ${PINS_GO}"
[[ -f "$TYPES_GO" ]] || die "missing ${TYPES_GO}"

# TOASTABLE=1 only when the tree can actually store an out-of-line ev_action:
# the pg_rewrite TOAST pair must be bootstrapped (2838/2839) AND the chunk
# writer must be present. Parsed out of the Go tree for the same reason the
# pins and types are — the script must not encode a capability the tree lost.
TOASTABLE=0
if grep -q '{Parent: 2618, ToastRel: 2838, ToastIdx: 2839' "$TOAST_BOOTSTRAP_GO" 2>/dev/null \
   && grep -q 'func externalizeVarlenaPayload' "$TOAST_WRITER_GO" 2>/dev/null; then
    TOASTABLE=1
fi

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
    < <(sed -n 's/^[[:space:]]*return pgTypeEntry{\([0-9]*\),.*/\1/p' "$TYPES_GO")
[[ ${#known_type[@]} -gt 0 ]] || die "parsed zero pg_type entries out of ${TYPES_GO}"

if [[ $PROCSQLBODY -eq 0 ]]; then
    if [[ $VERIFY -eq 1 ]]; then
        [[ ${#VIEWS[@]} -eq 0 ]] || die "--verify takes no view arguments"
        VIEWS=("${PINNED_ORDER[@]}")
    fi
    [[ ${#VIEWS[@]} -gt 0 ]] || die "no views named (try --verify)"
fi

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
# --prosqlbody mode (M0133-S2): capture the information_schema helper
# functions' pg_proc rows + prosqlbody node-tree blobs.
# -------------------------------------------------------------------------- #
if [[ $PROCSQLBODY -eq 1 ]]; then
    if [[ $VERIFY -eq 1 ]]; then
        [[ ${#VIEWS[@]} -eq 0 ]] || die "--prosqlbody --verify takes no OID arguments"
        # Re-derive the committed function set from the manifest's proc rows.
        VIEWS=()
        while IFS=$'\t' read -r kind name oid _; do
            [[ "$kind" == "proc" ]] && VIEWS+=("$oid")
        done < <(grep -v '^#' "$PROC_MANIFEST" 2>/dev/null || true)
        [[ ${#VIEWS[@]} -gt 0 ]] || die "--prosqlbody --verify: no committed proc rows in ${PROC_MANIFEST}"
    fi
    [[ ${#VIEWS[@]} -gt 0 ]] || die "--prosqlbody needs at least one function OID (or --verify)"

    PROC_MANIFEST_STAGE="${STAGE_DIR}/information_schema_proc_manifest.tsv"
    {
        echo "# Code generated by scripts/capture-ev-action.sh --prosqlbody; DO NOT EDIT."
        echo "# Oracle: PostgreSQL ${ORACLE_VERSION}, catalog version ${ORACLE_CATVERSION}."
        echo "# Captured from a throwaway 'initdb --no-sync' cluster (M0133-S2)."
        echo "#"
        echo "# proc <name> <oid> <pronamespace> <procost> <prorows> <prosupport> <prokind> <provolatile> <proparallel> <proisstrict> <proretset> <prolang> <prorettype> <proargtypes> <proallargtypes> <proargmodes> <proargnames> <prosrc> <prosqlbody>"
        echo "# <proargtypes> is space-separated oidvector text; <proallargtypes>/<proargmodes>/<proargnames> are PG array ::text or '-' for NULL; <prosqlbody> is '-' (NULL) or the blob's text length."
    } > "$PROC_MANIFEST_STAGE"

    for oid in "${VIEWS[@]}"; do
        echo "capture-ev-action: capturing prosqlbody for pg_proc OID ${oid}..."

        # Structural guard: every column this manifest does NOT carry must be the
        # value the seed path hardcodes (prosecdef/proleakproof false, owner 10,
        # no variadic, no arg defaults, no trftypes/probin/proconfig/proacl).
        # A helper that broke the assumption would silently seed a wrong row.
        guard_row="$(psql_q "SELECT (p.prosecdef OR p.proleakproof), (p.proowner <> 10), (p.provariadic <> 0), (p.pronargdefaults <> 0), (p.protrftypes IS NOT NULL), (p.probin IS NOT NULL), (p.proconfig IS NOT NULL), (p.proacl IS NOT NULL) FROM pg_proc p WHERE p.oid = ${oid}")" \
            || die "pg_proc guard query failed for OID ${oid}"
        IFS=$'\t' read -r g_secdef g_owner g_variadic g_nargdef g_trftypes g_probin g_proconfig g_proacl <<< "$guard_row"
        for bad in "$g_secdef" "$g_owner" "$g_variadic" "$g_nargdef" "$g_trftypes" "$g_probin" "$g_proconfig" "$g_proacl"; do
            [[ "$bad" == "f" ]] || fail "OID ${oid}: an unmodeled pg_proc column is non-default (guard row: ${guard_row})"
        done

        row="$(psql_q "SELECT p.proname, p.oid, p.pronamespace, p.procost, p.prorows,
                              p.prosupport::oid, p.prokind, p.provolatile, p.proparallel,
                              p.proisstrict, p.proretset, p.prolang, p.prorettype,
                              p.proargtypes::text,
                              coalesce(p.proallargtypes::text, '-'),
                              coalesce(p.proargmodes::text, '-'),
                              coalesce(p.proargnames::text, '-'),
                              CASE WHEN p.prosrc = '' THEN '<empty>' ELSE p.prosrc END,
                              CASE WHEN p.prosqlbody IS NULL THEN '-' ELSE length(p.prosqlbody::text)::text END
                         FROM pg_proc p WHERE p.oid = ${oid}")" \
            || die "pg_proc row query failed for OID ${oid}"
        IFS=$'\t' read -r p_name p_oid p_nsp p_cost p_rows p_support p_kind p_vol p_par \
            p_strict p_retset p_lang p_rettype p_argtypes p_allargs p_argmodes p_argnames \
            p_prosrc p_sqlbody <<< "$row"
        # '<empty>' is the capture-time sentinel for a zero-length prosrc: bash's
        # IFS whitespace collapsing would otherwise swallow an empty field that
        # sits between two tabs, shifting p_sqlbody into p_prosrc's slot.
        [[ "$p_prosrc" == "<empty>" ]] && p_prosrc=""
        [[ "$p_oid" == "$oid" ]] || fail "OID ${oid}: queried row carries oid ${p_oid}"
        [[ "$p_name" != "" ]] || fail "OID ${oid}: empty proname"
        # prosrc must not contain a tab or newline, or the TSV column splits.
        [[ "$p_prosrc" == "${p_prosrc//$'\t'/}" && "$p_prosrc" == "${p_prosrc//$'\n'/}" ]] \
            || fail "OID ${oid} (${p_name}): prosrc contains a tab/newline — the TSV manifest cannot carry it"

        # --- the prosqlbody blob (the node tree, captured verbatim) --------- #
        # Stored as PG's pg_proc.prosqlbody holds it: one line, no trailing
        # newline. First/last byte is NOT asserted as '('..')' the way
        # ev_action is — a single-statement SQL body nodeToString's to
        # '{QUERY ...}' while a BEGIN ATOMIC body is a parenthesised List.
        if [[ "$p_sqlbody" != "-" ]]; then
            raw="${STAGE_DIR}/${p_name}_prosqlbody.raw"
            dat="${STAGE_DIR}/${p_name}_prosqlbody.dat"
            psql -X -A -t -q -h "$SOCK_DIR" -d postgres -v ON_ERROR_STOP=1 \
                -c "SELECT prosqlbody FROM pg_proc WHERE oid = ${oid}" > "$raw" \
                || die "prosqlbody query failed for ${p_name}"
            [[ -s "$raw" ]] || fail "${p_name}: empty prosqlbody (declared length ${p_sqlbody})"
            [[ "$(tail -c1 "$raw" | od -An -tx1 | tr -d ' ')" == "0a" ]] \
                || fail "${p_name}: psql output did not end in a newline"
            head -c -1 "$raw" > "$dat"
            [[ "$(wc -l < "$dat")" -eq 0 ]] \
                || fail "${p_name}: prosqlbody spans multiple lines"
            [[ "$(wc -c < "$dat")" -eq "$p_sqlbody" ]] \
                || fail "${p_name}: prosqlbody captured $(wc -c < "$dat") B but the oracle stores ${p_sqlbody} B"
        fi

        printf 'proc\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
            "$p_name" "$p_oid" "$p_nsp" "$p_cost" "$p_rows" "$p_support" "$p_kind" "$p_vol" "$p_par" \
            "$p_strict" "$p_retset" "$p_lang" "$p_rettype" "$p_argtypes" "$p_allargs" "$p_argmodes" "$p_argnames" \
            "$p_prosrc" "$p_sqlbody" >> "$PROC_MANIFEST_STAGE"
    done

    if [[ $VERIFY -eq 1 ]]; then
        echo "capture-ev-action: --prosqlbody --verify: comparing against the committed tree..."
        rc=0
        for oid in "${VIEWS[@]}"; do
            info="$(grep -v '^#' "$PROC_MANIFEST_STAGE" | awk -F$'\t' -v o="$oid" '$1=="proc" && $3==o {print $2, $20}')"
            name="${info%% *}"; hasbody="${info##* }"
            [[ -n "$name" ]] || { echo "  MISSING  manifest row for OID ${oid}" >&2; rc=1; continue; }
            if [[ "$hasbody" == "-" ]]; then
                echo "  ok       ${name} (no prosqlbody — textual prosrc)"
                continue
            fi
            committed="${INITDB_DIR}/${name}_prosqlbody.dat"
            if [[ ! -f "$committed" ]]; then
                echo "  MISSING  ${committed}" >&2; rc=1; continue
            fi
            if cmp -s "${STAGE_DIR}/${name}_prosqlbody.dat" "$committed"; then
                echo "  ok       ${name}_prosqlbody.dat"
            else
                echo "  DRIFT    ${name}_prosqlbody.dat differs from the oracle" >&2
                rc=1
            fi
        done
        if diff <(grep -v '^#' "$PROC_MANIFEST_STAGE") <(grep -v '^#' "$PROC_MANIFEST") >/dev/null; then
            echo "  ok       $(basename "$PROC_MANIFEST")"
        else
            echo "  DRIFT    $(basename "$PROC_MANIFEST") differs from the oracle" >&2
            diff <(grep -v '^#' "$PROC_MANIFEST") <(grep -v '^#' "$PROC_MANIFEST_STAGE") >&2 || true
            rc=1
        fi
        [[ $rc -eq 0 ]] && echo "capture-ev-action: --prosqlbody --verify PASS (${#VIEWS[@]} procs byte-identical)"
        exit $rc
    fi

    mkdir -p "$PROC_OUT_DIR"
    for oid in "${VIEWS[@]}"; do
        name="$(grep -v '^#' "$PROC_MANIFEST_STAGE" | awk -F$'\t' -v o="$oid" '$1=="proc" && $3==o {print $2}')"
        if [[ -f "${STAGE_DIR}/${name}_prosqlbody.dat" ]]; then
            cp "${STAGE_DIR}/${name}_prosqlbody.dat" "${PROC_OUT_DIR}/${name}_prosqlbody.dat"
            echo "capture-ev-action: wrote ${PROC_OUT_DIR}/${name}_prosqlbody.dat"
        fi
    done
    cp "$PROC_MANIFEST_STAGE" "$PROC_MANIFEST"
    echo "capture-ev-action: wrote ${PROC_MANIFEST}"
    echo "capture-ev-action: --prosqlbody done (${#VIEWS[@]} procs)"
    exit 0
fi

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
    echo "capture-ev-action: capturing ${NSP_NAME}.${view}..."

    # --- Query 3: the pg_class row (feeds nailedRel) --------------------- #
    rel_row="$(psql_q "SELECT c.oid, c.reltype, c.relkind, c.relnatts
                         FROM pg_class c WHERE c.oid = '${NSP_NAME}.${view}'::regclass")" \
        || die "pg_class query failed for ${view}"
    IFS=$'\t' read -r rel_oid rel_type rel_kind rel_natts <<< "$rel_row"
    [[ -n "$rel_oid" ]] || die "${NSP_NAME}.${view} does not exist in the oracle"
    [[ "$rel_kind" == "v" ]] || fail "${view}: relkind is '${rel_kind}', not 'v'"

    rule_oid="$(psql_q "SELECT r.oid FROM pg_rewrite r
                         WHERE r.ev_class = '${NSP_NAME}.${view}'::regclass
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
             WHERE r.ev_class = '${NSP_NAME}.${view}'::regclass
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

    # Guard #5 (0131-0009 "Guards" §5, relaxed by M0131-S20.2b to "inline OR
    # toastable"): the seeded pg_rewrite row must be STORABLE. goopg keeps
    # ev_action as a PGLZ-compressed varlena (pgRewriteRow → pglzVarlenaDatum)
    # in base/{1,5}/2618; up to MAX_EV_ACTION_STORED that fits the tuple
    # inline, and above it S20.2a externalises the value into base/{1,5}/2838
    # behind an 18-byte VARTAG_ONDISK pointer. Measure the compressed size the
    # oracle itself stores (pg_column_size applies the same pglz).
    #
    # An over-budget capture is accepted only when BOTH hold:
    #   a) this tree still bootstraps the pair and carries the chunk writer
    #      (TOASTABLE above) — otherwise the pre-S20.2 hard failure stands; and
    #   b) the ORACLE stores this very value out of line in pg_toast_2618 under
    #      chunk_id = rule_oid + 1, with the chunk lengths summing to exactly
    #      pg_column_size.
    # (b) is not decoration: it is the same three facts the writer encodes
    # (F20 chunked bytes = compressed varlena minus its 4-byte header, so the
    # sum equals pg_column_size; F22 chunk_id = rule OID + 1), re-measured per
    # captured view against the oracle instead of trusted from the S20.2a run.
    # A capture that PG chose to keep inline while goopg would externalise it
    # (or vice versa) is caught here, not in a hosted PG's detoast path.
    stored_len="$(psql_q "SELECT pg_column_size(r.ev_action) FROM pg_rewrite r
                           WHERE r.ev_class = '${NSP_NAME}.${view}'::regclass
                             AND r.rulename = '_RETURN'")"
    [[ -n "$stored_len" ]] || die "${view}: pg_column_size query returned nothing"
    if [[ "$stored_len" -gt $MAX_EV_ACTION_STORED ]]; then
        [[ $TOASTABLE -eq 1 ]] \
            || fail "${view}: ev_action stores as ${stored_len} B compressed, over the ${MAX_EV_ACTION_STORED} B inline budget (MaxHeapTupleSize ${MAX_HEAP_TUPLE_SIZE} B minus ${TUPLE_OVERHEAD_BUDGET} B of header + the other pg_rewrite columns) — and this tree no longer carries the out-of-line writer (DECLARE_TOAST(pg_rewrite, 2838, 2839) in ${TOAST_BOOTSTRAP_GO} + externalizeVarlenaPayload in ${TOAST_WRITER_GO})"
        toast_row="$(psql_q "SELECT count(*), coalesce(sum(length(chunk_data)), 0)
                               FROM pg_toast.pg_toast_2618
                              WHERE chunk_id = ${rule_oid} + 1")"
        IFS=$'\t' read -r toast_chunks toast_bytes <<< "$toast_row"
        [[ "${toast_chunks:-0}" -gt 0 ]] \
            || fail "${view}: ev_action stores as ${stored_len} B (over the ${MAX_EV_ACTION_STORED} B inline budget) but the ORACLE holds no pg_toast_2618 chunks under chunk_id ${rule_oid}+1 — F22 (chunk_id == rule OID + 1) does not hold for this capture, so goopg would write the value under an OID upstream did not use"
        [[ "$toast_bytes" -eq "$stored_len" ]] \
            || fail "${view}: oracle chunk bytes ${toast_bytes} != pg_column_size ${stored_len} — F20 says the chunked bytes are the compressed varlena MINUS its 4-byte header, which makes these equal; a difference means the writer's off-by-four assumption is wrong for this value"
        echo "capture-ev-action:   ${view}: ${stored_len} B stored, OUT OF LINE (${toast_chunks} chunks, chunk_id ${rule_oid}+1)"
    fi

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
                      WHERE a.attrelid = '${NSP_NAME}.${view}'::regclass AND a.attnum > 0
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
