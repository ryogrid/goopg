#!/usr/bin/env bash
# Drive HammerDB to build the TPC-H schema (scale factor 1) against
# a running goopg cluster. Mirrors build_schema.sh.
#
# Pre-conditions:
#   - goopg must be up and reachable on $PG_HOST:$PG_PORT (run
#     setup_goopg.sh first).
#   - HammerDB-5.0 must be extracted at the repo root.
#
# This script reuses the same `tcl/build_schema.tcl` HammerDB
# script as the upstream-PG variant — connection settings come from
# environment variables, so the goopg run is configured purely via
# `env_goopg.sh`.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env_goopg.sh
source "${SCRIPT_DIR}/env_goopg.sh"

if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
    echo "goopg is not reachable at ${PG_HOST}:${PG_PORT}; run setup_goopg.sh first." >&2
    exit 1
fi

if [[ ! -x "${HAMMERDB_HOME}/hammerdbcli" ]]; then
    echo "HammerDB not found at ${HAMMERDB_HOME}; extract HammerDB-5.0 at repo root." >&2
    exit 1
fi

# HammerDB hardcodes ./scripts/... relative paths in its loaders.
cd "${HAMMERDB_HOME}"

ts="$(date +%Y%m%d-%H%M%S)"
log_file="${LOG_DIR}/build_goopg_${ts}.log"
echo "Building TPC-H schema against goopg; log: ${log_file}"

./hammerdbcli tcl auto "${SCRIPT_DIR}/tcl/build_schema.tcl" 2>&1 | tee "${log_file}"

echo "Schema build complete (or errored — check ${log_file})."

# M0142-0003i: add the canonical 8-constraint TPC-H FOREIGN KEY set —
# the same DDL the PG reference cluster at :65432 carries (verified via
# pg_get_constraintdef), including DEFERRABLE on lineitem_order_fk. The
# planner's get_foreign_key_join_selectivity arm (selfuncs.c port) reads
# declared pg_constraint rows, not just unique indexes, so these are what
# make FK-informed join row estimates (e.g. Q9's lineitem⋈partsupp) match
# PG's. HammerDB itself adds no FKs; a fresh build has none and a re-run
# reports "already exists" per statement while the count check below
# still verifies the live set (psql exits 0 on statement errors without
# ON_ERROR_STOP, which is why the check is what enforces).
PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" <<'SQL'
ALTER TABLE nation    ADD CONSTRAINT nation_region_fk     FOREIGN KEY (n_regionkey)          REFERENCES region(r_regionkey);
ALTER TABLE supplier  ADD CONSTRAINT supplier_nation_fk   FOREIGN KEY (s_nationkey)          REFERENCES nation(n_nationkey);
ALTER TABLE customer  ADD CONSTRAINT customer_nation_fk   FOREIGN KEY (c_nationkey)          REFERENCES nation(n_nationkey);
ALTER TABLE partsupp  ADD CONSTRAINT partsupp_part_fk     FOREIGN KEY (ps_partkey)           REFERENCES part(p_partkey);
ALTER TABLE partsupp  ADD CONSTRAINT partsupp_supplier_fk FOREIGN KEY (ps_suppkey)           REFERENCES supplier(s_suppkey);
ALTER TABLE orders    ADD CONSTRAINT order_customer_fk    FOREIGN KEY (o_custkey)            REFERENCES customer(c_custkey);
ALTER TABLE lineitem  ADD CONSTRAINT lineitem_partsupp_fk FOREIGN KEY (l_partkey, l_suppkey) REFERENCES partsupp(ps_partkey, ps_suppkey);
ALTER TABLE lineitem  ADD CONSTRAINT lineitem_order_fk    FOREIGN KEY (l_orderkey)           REFERENCES orders(o_orderkey) DEFERRABLE;
SQL
fk_count="$(PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" -tA -c "SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conname IN ('nation_region_fk','supplier_nation_fk','customer_nation_fk','partsupp_part_fk','partsupp_supplier_fk','order_customer_fk','lineitem_partsupp_fk','lineitem_order_fk')")"
if [[ "${fk_count}" != "8" ]]; then
    echo "fk_check: FAIL (${fk_count}/8 FK constraints present)" >&2
    exit 1
fi
echo "fk_check: OK (8/8 FK constraints present)"

# M0125-0030: verify HammerDB's ANALYZE populated stats and checkpoint.
# HammerDB's buildschema internally runs a "GATHERING SCHEMA STATISTICS" step
# that calls ANALYZE on each table. Before M0125-0028 this step errored 42P01
# in per-DB databases; with -0028/-0029 it should succeed and the counts must
# survive a restart, so we verify here and make them durable.
PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" -tA <<'SQL'
SELECT 'reltuples_check: ' ||
       CASE WHEN count(*) = 8 THEN 'OK (' || count(*)::text || '/8 tables have reltuples > 0)'
            ELSE 'FAIL (' || count(*)::text || '/8 tables have reltuples > 0)'
       END
FROM pg_class
WHERE relname IN ('lineitem','orders','partsupp','part','customer','supplier','nation','region')
  AND reltuples > 0;
SQL
echo "Running CHECKPOINT..."
PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" -c "CHECKPOINT" >/dev/null 2>&1 \
    && echo "CHECKPOINT done" \
    || echo "CHECKPOINT failed (non-fatal)"
