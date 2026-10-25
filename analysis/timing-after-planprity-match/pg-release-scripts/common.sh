# Shared helpers for the private non-cassert PG 18.3 clusters (bench/pg-release).
# Absolute paths only — never source bench/tpch/env.sh or bench/tpcds/env_tpcds.sh
# (they export PGPORT=65432 / put the cassert local_install first on PATH).
REPO=/home/ryo/work/goopg/goopg
B=$REPO/bench/pg-release
REL=$B/install/bin                      # non-cassert server build
CASS=$REPO/postgres/local_install/bin   # cassert build (reference clusters' binary)
CLI=$REPO/postgres/local_install/bin    # client tools (psql/pg_dump) — same as the goopg runs
TPCH_DATA=$B/data-tpch;  TPCH_PORT=5554; TPCH_SOCK=$B/sock-tpch
TPCDS_DATA=$B/data-tpcds; TPCDS_PORT=5556; TPCDS_SOCK=$B/sock-tpcds

# pg_start <bindir> <datadir> <port> <unit-label> : start postgres in the cgroup cap, wait, verify identity.
pg_start() {
  local bin=$1 dir=$2 port=$3 label=$4 user=${5:-postgres}
  ( cd $REPO && GOOPG_CG_UNIT=pgrel-$label-$(date +%H%M%S) nohup scripts/goopg-test-run.sh \
      $bin/postgres -D $dir > $B/logs/server-$label.log 2>&1 & )
  for _ in $(seq 1 120); do $CLI/pg_isready -h 127.0.0.1 -p $port -U $user -q && break; sleep 1; done
  local id
  id=$($CLI/psql -X -h 127.0.0.1 -p $port -U $user -d postgres -Atc \
       "select current_setting('data_directory')||'|'||current_setting('port')||'|'||current_setting('debug_assertions')||'|'||version()")
  echo "started: $id"
  case "$id" in "$dir|$port|"*) ;; *) echo "IDENTITY MISMATCH on port $port" >&2; return 1;; esac
}
pg_stop() { $REL/pg_ctl stop -D "$1" -m fast -w -t 120 >/dev/null 2>&1 || $CASS/pg_ctl stop -D "$1" -m fast -w -t 120; }
loadinfo() { echo "$(date '+%F %T') load=$(cut -d' ' -f1-3 /proc/loadavg) slice_high=$(awk '/^high/{print $2}' /sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/goopg.slice/goopg-workloads.slice/memory.events 2>/dev/null) ralph_heavy=$(pgrep -fc 'tpcds-sf025-regression.sh|tpcds-bench-compare.sh|run-nightly.sh')"; }
