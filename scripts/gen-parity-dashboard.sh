#!/usr/bin/env bash
#
# gen-parity-dashboard.sh — compare goopg's PG 18.3 compatibility surface.
#
# Produces docs/parity-dashboard.md with three sections:
#   1. GUC parity     — which postgresql.conf parameters goopg implements
#                       vs the 469 in PG 18.3's guc_tables.c.
#   2. SQLSTATE parity — which 5-char error codes are declared in
#                        internal/sqlstate/codes.go vs PG's errcodes.txt.
#   3. pg_catalog      — system catalog object names referenced in goopg
#                        vs the full pg_catalog table inventory from PG.
#
# Usage:
#   scripts/gen-parity-dashboard.sh            # writes docs/parity-dashboard.md
#   scripts/gen-parity-dashboard.sh --stdout   # print to stdout instead
#
# This script is intentionally read-only (no go build, no servers) so it
# can run fast as a development tool and in CI. It uses grep/awk over
# source files — never queries a live database.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

TO_STDOUT=0
[[ "${1:-}" == "--stdout" ]] && TO_STDOUT=1

OUT_FILE="${REPO_ROOT}/docs/parity-dashboard.md"

# -------------------------------------------------------------------------- #
# Source files
# -------------------------------------------------------------------------- #
PG_GUC_SRC="${REPO_ROOT}/postgres/src/backend/utils/misc/guc_tables.c"
PG_ERRCODES="${REPO_ROOT}/postgres/src/backend/utils/errcodes.txt"
GOOPG_GUC_SRC="${REPO_ROOT}/internal/config/defaults.go"
GOOPG_SQLSTATE_SRC="${REPO_ROOT}/internal/sqlstate/codes.go"

# pg_catalog table list: extract from goopg's catalog.go and from PG's
# pg_catalog schema definition files.
GOOPG_CATALOG_SRC="${REPO_ROOT}/internal/catalog/catalog.go"
PG_CATALOG_SRC_DIR="${REPO_ROOT}/postgres/src/include/catalog"

for f in "$PG_GUC_SRC" "$PG_ERRCODES" "$GOOPG_GUC_SRC" "$GOOPG_SQLSTATE_SRC"; do
    if [[ ! -f "$f" ]]; then
        echo "gen-parity-dashboard: missing source file: $f" >&2
        exit 2
    fi
done

# -------------------------------------------------------------------------- #
# 1. GUC parity
# -------------------------------------------------------------------------- #

# PG 18.3: extract GUC names from guc_tables.c
# Format: lines like:  {"application_name", PGC_USERSET, ...
pg_gucs=$(
    grep -oP '^\s*\{"?\K[a-z][a-z_0-9]*(?=",)' "$PG_GUC_SRC" \
    | sort -u
)
pg_guc_count=$(echo "$pg_gucs" | grep -c .)

# goopg: extract GUC names from internal/config/defaults.go
# Format: Name: "application_name",
goopg_gucs=$(
    grep -oP '(?<=Name: ")[^"]+' "$GOOPG_GUC_SRC" \
    | tr '[:upper:]' '[:lower:]' \
    | sort -u
)
goopg_guc_count=$(echo "$goopg_gucs" | grep -c . || echo 0)

# Find implemented (intersection) and missing (in PG but not goopg)
implemented_gucs=$(comm -12 \
    <(echo "$pg_gucs") \
    <(echo "$goopg_gucs"))
missing_gucs=$(comm -23 \
    <(echo "$pg_gucs") \
    <(echo "$goopg_gucs"))
extra_gucs=$(comm -13 \
    <(echo "$pg_gucs") \
    <(echo "$goopg_gucs"))

impl_guc_count=$(echo "$implemented_gucs" | grep -c . || echo 0)
missing_guc_count=$(echo "$missing_gucs" | grep -c . || echo 0)
extra_guc_count=$(echo "$extra_gucs" | grep -c . || echo 0)

guc_pct=$(awk "BEGIN {printf \"%.1f\", ${impl_guc_count}*100/${pg_guc_count}}")

# -------------------------------------------------------------------------- #
# 2. SQLSTATE parity
# -------------------------------------------------------------------------- #

# PG: extract 5-char codes from errcodes.txt
# Format: 00000    S    ERRCODE_...    name
pg_states=$(
    grep -v '^#' "$PG_ERRCODES" \
    | grep -v '^$' \
    | grep -v '^Section' \
    | awk '{print $1}' \
    | sort -u
)
pg_state_count=$(echo "$pg_states" | grep -c .)

# goopg: extract 5-char SQLSTATE codes from codes.go
# Format: SomeCode Code = "00000" // name
goopg_states=$(
    grep -oP '"[0-9A-Z]{5}"' "$GOOPG_SQLSTATE_SRC" \
    | tr -d '"' \
    | sort -u
)
goopg_state_count=$(echo "$goopg_states" | grep -c . || echo 0)

implemented_states=$(comm -12 \
    <(echo "$pg_states") \
    <(echo "$goopg_states"))
missing_states=$(comm -23 \
    <(echo "$pg_states") \
    <(echo "$goopg_states"))

impl_state_count=$(echo "$implemented_states" | grep -c . || echo 0)
missing_state_count=$(echo "$missing_states" | grep -c . || echo 0)

state_pct=$(awk "BEGIN {printf \"%.1f\", ${impl_state_count}*100/${pg_state_count}}")

# -------------------------------------------------------------------------- #
# 3. pg_catalog table parity
# -------------------------------------------------------------------------- #

# PG 18.3: extract catalog table names from pg_catalog header files
# Pattern: CATALOG(pg_class,...) in postgres/src/include/catalog/pg_*.h
pg_catalogs=$(
    if [[ -d "$PG_CATALOG_SRC_DIR" ]]; then
        grep -rh 'CATALOG(\(pg_[a-z_]*\)' "$PG_CATALOG_SRC_DIR" 2>/dev/null \
        | grep -oP 'CATALOG\(\K[a-z_]+' \
        | sort -u
    else
        echo ""
    fi
)
pg_catalog_count=$(echo "$pg_catalogs" | grep -c . || echo 0)

# goopg: extract catalog table names referenced in catalog.go
# Heuristic: string literals matching "pg_[a-z_]+"
goopg_catalogs=""
if [[ -f "$GOOPG_CATALOG_SRC" ]]; then
    goopg_catalogs=$(
        grep -oP '"pg_[a-z_]+"' "$GOOPG_CATALOG_SRC" \
        | tr -d '"' \
        | sort -u
    )
fi
goopg_catalog_count=$(echo "$goopg_catalogs" | grep -c . || echo 0)

implemented_catalogs=""
missing_catalogs="$pg_catalogs"  # default: all missing
if [[ -n "$pg_catalogs" && -n "$goopg_catalogs" ]]; then
    implemented_catalogs=$(comm -12 \
        <(echo "$pg_catalogs") \
        <(echo "$goopg_catalogs"))
    missing_catalogs=$(comm -23 \
        <(echo "$pg_catalogs") \
        <(echo "$goopg_catalogs"))
fi
impl_catalog_count=$(echo "$implemented_catalogs" | grep -c . || echo 0)
missing_catalog_count=$(echo "$missing_catalogs" | grep -c . || echo 0)

catalog_pct=0
[[ $pg_catalog_count -gt 0 ]] && \
    catalog_pct=$(awk "BEGIN {printf \"%.1f\", ${impl_catalog_count}*100/${pg_catalog_count}}")

# -------------------------------------------------------------------------- #
# Render markdown
# -------------------------------------------------------------------------- #
# Note: eval'ing a HERE-doc with variables — all $ are from shell, no external input
DATESTAMP="$(date '+%Y-%m-%d')"

render() {
cat <<MARKDOWN
# goopg PG 18.3 Parity Dashboard

> Generated: ${DATESTAMP}
> Source: PG 18.3 (postgres/), goopg HEAD

---

## Summary

| Area | goopg | PG 18.3 | Parity |
|------|------:|--------:|-------:|
| GUC parameters | ${goopg_guc_count} | ${pg_guc_count} | ${guc_pct}% |
| SQLSTATE codes | ${goopg_state_count} | ${pg_state_count} | ${state_pct}% |
| pg_catalog tables | ${impl_catalog_count} | ${pg_catalog_count} | ${catalog_pct}% |

---

## 1. GUC Parameters (${impl_guc_count}/${pg_guc_count} = ${guc_pct}%)

goopg implements ${goopg_guc_count} of PG's ${pg_guc_count} GUC parameters.
${extra_guc_count} entries in goopg have no direct PG counterpart (extensions or goopg-specific).

### Missing (${missing_guc_count})

<details>
<summary>Click to expand ${missing_guc_count} unimplemented GUCs</summary>

\`\`\`
$(echo "$missing_gucs")
\`\`\`

</details>

### Implemented (${impl_guc_count})

<details>
<summary>Click to expand ${impl_guc_count} implemented GUCs</summary>

\`\`\`
$(echo "$implemented_gucs")
\`\`\`

</details>

---

## 2. SQLSTATE Codes (${impl_state_count}/${pg_state_count} = ${state_pct}%)

goopg declares ${goopg_state_count} of PG's ${pg_state_count} SQLSTATE codes
in \`internal/sqlstate/codes.go\` (generated from the same \`errcodes.txt\`).

### Missing (${missing_state_count})

<details>
<summary>Click to expand ${missing_state_count} missing SQLSTATE codes</summary>

\`\`\`
$(echo "$missing_states")
\`\`\`

</details>

---

## 3. pg_catalog Tables (${impl_catalog_count}/${pg_catalog_count} = ${catalog_pct}%)

pg_catalog tables referenced in \`internal/catalog/catalog.go\` vs
PG 18.3 CATALOG() declarations in \`postgres/src/include/catalog/\`.

### Missing (${missing_catalog_count})

<details>
<summary>Click to expand ${missing_catalog_count} missing catalog tables</summary>

\`\`\`
$(echo "$missing_catalogs")
\`\`\`

</details>

### Implemented (${impl_catalog_count})

\`\`\`
$(echo "$implemented_catalogs")
\`\`\`

---

*Re-generate with: \`make parity-dashboard\` or \`scripts/gen-parity-dashboard.sh\`*
MARKDOWN
}

if [[ "$TO_STDOUT" -eq 1 ]]; then
    render
else
    mkdir -p "$(dirname "${OUT_FILE}")"
    render > "${OUT_FILE}"
    echo "gen-parity-dashboard: wrote ${OUT_FILE}"
    echo "  GUC:      ${guc_pct}% (${impl_guc_count}/${pg_guc_count})"
    echo "  SQLSTATE: ${state_pct}% (${impl_state_count}/${pg_state_count})"
    echo "  Catalog:  ${catalog_pct}% (${impl_catalog_count}/${pg_catalog_count})"
fi
