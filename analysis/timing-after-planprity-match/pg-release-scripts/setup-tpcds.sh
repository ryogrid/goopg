#!/usr/bin/env bash
# Private TPC-DS cluster = byte-exact pg_basebackup of the :65438 reference (read-only w.r.t. its data;
# AGENT.md R1 allows pg_basebackup on reference clusters). Contains dbs tpcds025 (used) and tpcds (SF1, unused).
set -euo pipefail
source "$(dirname "$0")/common.sh"
[[ -e $TPCDS_DATA ]] && { echo "$TPCDS_DATA exists"; exit 1; }
mkdir -p $TPCDS_SOCK
echo "$(date +%T) basebackup start"
$CLI/pg_basebackup -h 127.0.0.1 -p 65438 -U ryo -D $TPCDS_DATA -X stream -c fast -P 2> $B/logs/basebackup-tpcds.log
echo "$(date +%T) basebackup done"
rm -f $TPCDS_DATA/standby.signal $TPCDS_DATA/postmaster.pid
cat >> $TPCDS_DATA/postgresql.conf <<CONF
# ---- bench/pg-release overrides (README §6): identity + goopg alignment ----
listen_addresses = '127.0.0.1'
port = $TPCDS_PORT
unix_socket_directories = '$TPCDS_SOCK'
shared_buffers = 128MB        # aligned to goopg's data-sf025 (README §3.2 item 1)
jit = off
CONF
: > $TPCDS_DATA/postgresql.auto.conf
pg_start $REL $TPCDS_DATA $TPCDS_PORT tpcds-check
$CLI/psql -X -h $TPCDS_SOCK -p $TPCDS_PORT -U ryo -d tpcds025 -Atc "show shared_buffers; show work_mem; show max_parallel_workers_per_gather; select count(*) from store_sales"
pg_stop $TPCDS_DATA
echo SETUP-TPCDS-DONE
