#!/usr/bin/env bash
# Build the private TPC-H SF1 cluster from the :65432 reference (read-only pg_dump) — see README §6.
set -euo pipefail
source "$(dirname "$0")/common.sh"
[[ -e $TPCH_DATA ]] && { echo "$TPCH_DATA exists"; exit 1; }
mkdir -p $TPCH_SOCK $B/dump
$REL/initdb -D $TPCH_DATA -E UTF8 --locale=C -U postgres > $B/logs/initdb-tpch.log 2>&1
cat >> $TPCH_DATA/postgresql.conf <<CONF
# ---- bench/pg-release (mirrors :65432 + goopg alignment, README §6) ----
listen_addresses = '127.0.0.1'
port = $TPCH_PORT
unix_socket_directories = '$TPCH_SOCK'
max_connections = 64
shared_buffers = 2048MB
work_mem = 512MB
maintenance_work_mem = 256MB
effective_cache_size = 2GB
checkpoint_timeout = 15min
max_wal_size = 4GB
wal_compression = on
max_parallel_workers_per_gather = 4
max_parallel_workers = 8
max_worker_processes = 16
jit = off
include_if_exists = 'load_phase.conf'
CONF
printf "autovacuum = off\nwal_level = minimal\nmax_wal_senders = 0\n" > $TPCH_DATA/load_phase.conf
cat > $TPCH_DATA/pg_hba.conf <<HBA
local   all             all                                     trust
host    all             all             127.0.0.1/32            trust
HBA
pg_start $REL $TPCH_DATA $TPCH_PORT tpch-load
P="$CLI/psql -X -v ON_ERROR_STOP=1 -h $TPCH_SOCK -p $TPCH_PORT -U postgres"
$P -d postgres -c "create role tpch login password 'tpch'" \
   -c "create database tpch owner tpch template template0 encoding 'UTF8' locale 'C'"
# READ-ONLY against the reference: pg_dump only.
echo "$(date +%T) dump start"
PGPASSWORD=tpch $CLI/pg_dump -h 127.0.0.1 -p 65432 -U tpch -d tpch -t public.lineitem -t public.orders -t public.customer -t public.part -t public.partsupp -t public.supplier -t public.nation -t public.region -Fd -j 4 -Z 0 -v --no-statistics -f $B/dump/tpch-data.dir 2> $B/logs/pg_dump-tpch.log
PGPASSWORD=tpch $CLI/pg_dump -h 127.0.0.1 -p 65432 -U tpch -d tpch -t public.lineitem -t public.orders -t public.customer -t public.part -t public.partsupp -t public.supplier -t public.nation -t public.region -Fc --statistics-only -f $B/dump/tpch-stats.dump
echo "$(date +%T) dump done"
$CLI/pg_restore -h $TPCH_SOCK -p $TPCH_PORT -U postgres -d tpch -j 4 -v --exit-on-error $B/dump/tpch-data.dir 2> $B/logs/pg_restore-tpch.log
echo "$(date +%T) restore done"
$P -d tpch -c "vacuum (analyze)"
echo "$(date +%T) vacuum done"
# The stats import only SETS fields present in the source, so first clear what our own ANALYZE
# produced (else e.g. an MCV list the reference does not have survives: part.p_retailprice).
$P -d tpch -Atc "select count(*) from (select pg_clear_attribute_stats('public', c.relname, a.attname, false) from pg_class c join pg_attribute a on a.attrelid=c.oid where c.relnamespace='public'::regnamespace and c.relkind='r' and a.attnum>0 and not a.attisdropped) s"
$CLI/pg_restore -h $TPCH_SOCK -p $TPCH_PORT -U postgres -d tpch --exit-on-error $B/dump/tpch-stats.dump
$P -d tpch -c checkpoint
pg_stop $TPCH_DATA
rm -f $TPCH_DATA/load_phase.conf
echo SETUP-TPCH-DONE
