#!/usr/bin/env bash
# tpcds-setup.sh — State 1→2: Compile TPC-DS tools, generate SF=1 data,
# convert to tab-delimited TSV (COPY-ready), generate PG-compatible queries.
#
# State 1 prerequisite: third-party/tpcds-postgres exists (git clone of
#   https://github.com/celuk/tpcds-postgres.git)
# State 2 outcome:
#   - dsdgen + dsqgen compiled
#   - SF=1 .dat files under DSGen-software-code-3.2.0rc1/tools/
#   - COPY-ready .tsv files under bench/tpch/runtime_goopg/tpcds-data/
#   - PG-fixed query files under bench/tpch/runtime_goopg/tpcds-data/queries/
#
# Usage: scripts/tpcds-setup.sh [SCALE]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

SCALE="${1:-1}"
TPCDS_DIR="${REPO_ROOT}/third-party/tpcds-postgres"
TPCDS_TOOLS="${TPCDS_DIR}/DSGen-software-code-3.2.0rc1/tools"
TPCDS_DATA_DIR="${RUNTIME_DIR}/tpcds-data"
TPCDS_QUERY_DIR="${TPCDS_DATA_DIR}/queries"

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

# ---- Prerequisites check ------------------------------------------
[[ -d "${TPCDS_DIR}" ]] || die "${TPCDS_DIR} not found — clone https://github.com/celuk/tpcds-postgres.git first"
[[ -f "${TPCDS_TOOLS}/makefile" ]] || die "TPC-DS toolkit not found at ${TPCDS_TOOLS}"

mkdir -p "${TPCDS_DATA_DIR}" "${TPCDS_QUERY_DIR}"

# ---- Step 1: Compile TPC-DS toolkit -------------------------------
log "Step 1/6: Compiling TPC-DS toolkit..."
if [[ ! -x "${TPCDS_TOOLS}/dsdgen" ]] || [[ ! -x "${TPCDS_TOOLS}/dsqgen" ]]; then
    cd "${TPCDS_TOOLS}"
    # Ensure -fcommon is set for GCC>=10 (multiple definition errors)
    if ! grep -q 'fcommon' makefile 2>/dev/null; then
        sed -i 's/BASE_CFLAGS    = -D_FILE_OFFSET_BITS=64 -D_LARGEFILE_SOURCE -DYYDEBUG /BASE_CFLAGS    = -D_FILE_OFFSET_BITS=64 -D_LARGEFILE_SOURCE -DYYDEBUG -fcommon /' makefile
    fi
    make clean 2>/dev/null || true
    make OS=LINUX 2>&1 | tail -3
    cd "${REPO_ROOT}"
    [[ -x "${TPCDS_TOOLS}/dsdgen" ]] || die "dsdgen build failed"
    [[ -x "${TPCDS_TOOLS}/dsqgen" ]] || die "dsqgen build failed"
    log "  Tools compiled"
else
    log "  Tools already compiled"
fi

# ---- Step 2: Generate SF=1 data (dsdgen) --------------------------
log "Step 2/6: Generating TPC-DS SF=${SCALE} data..."
cd "${TPCDS_TOOLS}"
./dsdgen -FORCE -VERBOSE -SCALE "${SCALE}" 2>&1 | tail -3
cd "${REPO_ROOT}"
DAT_COUNT=$(ls "${TPCDS_TOOLS}"/*.dat 2>/dev/null | wc -l)
log "  Generated ${DAT_COUNT} .dat files"

# ---- Step 3: Convert .dat → COPY-ready .tsv -----------------------
log "Step 3/6: Converting .dat to COPY-ready .tsv..."
for dat in "${TPCDS_TOOLS}"/*.dat; do
    table=$(basename "$dat" .dat)
    # Fix customer.dat encoding (UTF-8 corruption from dsdgen)
    if [[ "$table" == "customer" ]]; then
        python3 "${TPCDS_DIR}/fix_encoding.py" --filename="$dat" 2>/dev/null || true
    fi
    # Convert pipe-delimited → tab-delimited, empty fields → \N
    python3 "${SCRIPT_DIR}/convert_tpcds.py" "$dat" > "${TPCDS_DATA_DIR}/${table}.tsv"
    echo "  ${table}.tsv"
done
TSV_COUNT=$(ls "${TPCDS_DATA_DIR}"/*.tsv 2>/dev/null | wc -l)
log "  Converted ${TSV_COUNT} .tsv files"

# ---- Step 4: Generate queries (dsqgen) ----------------------------
log "Step 4/6: Generating TPC-DS queries..."
cd "${TPCDS_TOOLS}"
./dsqgen -DIRECTORY ../query_templates -INPUT ../query_templates/templates.lst \
    -VERBOSE Y -QUALIFY Y -DIALECT netezza -SCALE "${SCALE}" -OUTPUT_DIR "${TPCDS_QUERY_DIR}" 2>&1 | tail -3
cd "${REPO_ROOT}"
[[ -f "${TPCDS_QUERY_DIR}/query_0.sql" ]] || die "dsqgen failed — no query_0.sql produced"
log "  query_0.sql generated"

# ---- Step 5: Split and fix queries for PostgreSQL -----------------
log "Step 5/6: Splitting and fixing queries for PostgreSQL..."
cd "${TPCDS_QUERY_DIR}"

# Split query_0.sql into query1.sql .. query99.sql
python3 "${SCRIPT_DIR}/tpcds_split_queries.py" query_0.sql

QUERY_COUNT=$(ls query[0-9]*.sql 2>/dev/null | wc -l)
log "  Split into ${QUERY_COUNT} query files"

# Apply PG compatibility fixes (same as split_sqls.py + days fix)
log "  Applying PG compatibility fixes..."
for f in query*.sql; do
    qnum=$(echo "$f" | grep -oP '\d+')
    # Fix 1: netezza 'N days' → PG 'INTERVAL ''N days'''
    sed -i -E "s/([+-] *)([0-9]+) days/\\1INTERVAL '\\2 days'/g" "$f"

    # Fix 2: query 30 — c_last_review_date_sk → c_last_review_date
    [[ "$qnum" == "30" ]] && sed -i 's/c_last_review_date_sk/c_last_review_date/g' "$f"

    # Fix 3: queries needing subquery wrapper (column alias in WHERE)
    case "$qnum" in 36|70|86)
        mv "$f" "${f}.orig"
        { echo "select * from ("; cat "${f}.orig"; echo ") as sub"; } > "$f"
        rm "${f}.orig"
        ;;
    esac
done
cd "${REPO_ROOT}"
log "  Queries fixed and ready"

# ---- Step 6: Verify -------------------------------------------------
log "Step 6/6: Verification"
log "  Data files: ${TPCDS_DATA_DIR}/*.tsv (${TSV_COUNT} files)"
log "  Query files: ${TPCDS_QUERY_DIR}/query*.sql (${QUERY_COUNT} files)"
log ""
log "=== SETUP COMPLETE ==="
log "Next: scripts/tpcds-load.sh   (load data into goopg)"
log "Then: scripts/tpcds-run.sh    (run benchmark)"
