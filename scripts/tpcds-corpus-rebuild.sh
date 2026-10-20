#!/usr/bin/env bash
# tpcds-corpus-rebuild.sh — OWNER-ONLY. Rebuild one shipped goopg TPC-DS corpus
# into a STAGING data dir on the current engine, carrying the PG reference's
# own statistics. It never touches the shipped data dirs; the swap is a
# separate owner step (bench/tpcds/README.md "Rebuilding the shipped corpora").
#
# Usage: scripts/tpcds-corpus-rebuild.sh <sf025|sf1> <staging-dir> <port> [goopg-bin]
#
# Procedure (owner decision 2026-10-19, M0146-0215 / M0146-0216):
#   1. goopg init; copy postgresql.conf / postgresql.auto.conf / pg_hba.conf
#      from the shipped dir.
#   2. tpcds.sql (keys inline -> insert-built indexes, as the reference), then
#      server-side COPY per table from the same TSVs the reference loaded.
#   3. VACUUM per table: truncates COPY's bulk-extension heap tail, as the
#      reference's autovacuum did (relpages must equal the reference's).
#   4. NO ANALYZE. pg_dump --statistics-only from :65438 (tpcds025 / tpcds),
#      minus the reference's stray `bt` table, imported through
#      pg_restore_relation_stats / pg_restore_attribute_stats. PG's import
#      MERGES into existing statistics (attribute_stats.c upsert), so importing
#      over a goopg ANALYZE would leave goopg slots PG lacks; pg_restore and
#      pg_upgrade import into empty statistics, and so does this.
#   5. CHECKPOINT and two restarts (the import must survive the pg_statistic
#      reload and the size sidecar); the server is left STOPPED.
# Verify afterwards against the reference before any swap (row counts, heap
# relpages, index pages, pg_stats) — see README.
set -euo pipefail
[[ $# -ge 3 ]] || { sed -n '2,26p' "$0"; exit 2; }
CORPUS="$1"; NEW="$2"; PORT="$3"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${REPO}/bench/tpcds/env_tpcds.sh"
BIN="${4:-${GOOPG_BIN}}"
[[ -x "${BIN}" ]] || { echo "goopg binary ${BIN} missing (build it: go build -o <path> ./cmd/goopg)" >&2; exit 2; }
case "${CORPUS}" in
  sf025) SHIPPED="${SF025_GOOPG_DATA}"; TSV="${SF025_DATA_DIR}"; PGDB="${SF025_PG_DB}" ;;
  sf1)   SHIPPED="${TPCDS_PGDATA}";     TSV="${TPCDS_DATA_DIR}"; PGDB="${TPCDS_PG_DB}" ;;
  *) echo "corpus must be sf025 or sf1" >&2; exit 2 ;;
esac
[[ ! -e "${NEW}" ]] || { echo "staging dir ${NEW} exists — remove it first" >&2; exit 2; }
LOG="${NEW}.server.log"
UNIT="goopg-corpus-rebuild-${CORPUS}"
TABLES="call_center catalog_page catalog_returns catalog_sales customer \
customer_address customer_demographics date_dim household_demographics \
income_band inventory item promotion reason ship_mode store store_returns \
store_sales time_dim warehouse web_page web_returns web_sales web_site"
P="psql -X -q -h 127.0.0.1 -p ${PORT} -U postgres -d postgres -v ON_ERROR_STOP=1"
log() { echo "[$(date +%T)] $*"; }

pg_isready -h 127.0.0.1 -p "${PORT}" -q 2>/dev/null && { echo "port ${PORT} busy" >&2; exit 3; }
STATS="$(mktemp)"
trap 'rm -f "${STATS}" "${STATS}.raw"' EXIT
pg_dump --statistics-only -h "${TPCDS_HOST}" -p "${TPCDS_PG_PORT}" -U "${TPCDS_PG_USER}" -d "${PGDB}" -f "${STATS}.raw"
python3 - "${STATS}.raw" "${STATS}" <<'EOF'
import re, sys
s = open(sys.argv[1]).read()
blocks = re.findall(r"SELECT \* FROM pg_catalog\.pg_restore_(?:relation|attribute)_stats\(\n.*?\n\);\n", s, re.S)
keep = [b for b in blocks if not re.search(r"'relname', 'bt'", b)]
open(sys.argv[2], "w").write("".join(keep))
print(f"reference stats: {len(blocks)} calls, {len(keep)} kept")
EOF

"${BIN}" init -D "${NEW}" >/dev/null
cp "${SHIPPED}/postgresql.conf" "${SHIPPED}/postgresql.auto.conf" "${SHIPPED}/pg_hba.conf" "${NEW}/"

start_srv() {
  systemctl --user reset-failed "${UNIT}.scope" >/dev/null 2>&1 || true
  ( export GOMEMLIMIT="${GOMEMLIMIT_REBUILD:-12GiB}" GOGC=100 GOOPG_CG_UNIT="${UNIT}"
    setsid "${REPO}/scripts/goopg-test-run.sh" "${BIN}" start -D "${NEW}" \
      --listen "127.0.0.1:${PORT}" --hba "${NEW}/pg_hba.conf" </dev/null >>"${LOG}" 2>&1 & )
  for _ in $(seq 1 300); do pg_isready -h 127.0.0.1 -p "${PORT}" -U postgres -q && return 0; sleep 1; done
  echo "server not ready; see ${LOG}" >&2; exit 5
}
stop_srv() {
  "${BIN}" stop -D "${NEW}" >>"${LOG}" 2>&1 || true
  for _ in $(seq 1 120); do [[ -f "${NEW}/postmaster.pid" ]] || break; sleep 1; done
  systemctl --user stop "${UNIT}.scope" >/dev/null 2>&1 || true
}

start_srv
${P} -f "${TPCDS_TOOLS}/tpcds.sql" >/dev/null
for t in ${TABLES}; do
  ${P} -c "COPY ${t} FROM '${TSV}/${t}.tsv'"
  log "COPY ${t} $(${P} -At -c "SELECT count(*) FROM ${t}")"
done
for t in ${TABLES}; do ${P} -c "VACUUM ${t}"; done
log "VACUUM done"
res=$(${P} -At -f "${STATS}" 2>&1)
nt=$(grep -c '^t$' <<<"${res}" || true); nf=$(grep -c '^f$' <<<"${res}" || true); ne=$(grep -cE 'ERROR|WARNING' <<<"${res}" || true)
log "stats import: ${nt} true, ${nf} false, ${ne} ERROR/WARNING"
${P} -c "CHECKPOINT"
stop_srv; start_srv; stop_srv; start_srv; stop_srv
log "staging corpus ready (stopped): ${NEW}"
[[ "${nf}" == 0 && "${ne}" == 0 ]] || { echo "import was not clean" >&2; exit 1; }
