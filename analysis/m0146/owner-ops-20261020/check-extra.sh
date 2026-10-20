#!/usr/bin/env bash
# check-extra.sh <goopg_port> <pg_db> : amcheck on all pkeys + per-block heap row counts vs PG
set -u
export PATH=/home/ryo/work/goopg/goopg/postgres/local_install/bin:$PATH
G="psql -X -At -h 127.0.0.1 -p $1 -U postgres -d postgres"
P="psql -X -At -h 127.0.0.1 -p 65438 -U ryo -d $2"
bad=0
for i in $($G -c "select relname from pg_class where relkind='i' and relname like '%_pkey' order by 1"); do
  out=$($G -c "select bt_index_parent_check('$i'::regclass, true, false, true)" 2>&1) || { echo "AMCHECK FAIL $i: $out"; bad=1; }
done
echo "amcheck: $($G -c "select count(*) from pg_class where relkind='i' and relname like '%_pkey'") pkeys checked, bad=$bad"
for t in store_sales store_returns catalog_sales; do
  q="select (ctid::text::point)[0]::int b, count(*) from $t group by 1 order by 1"
  d=$(diff <($G -F, -c "$q") <($P -F, -c "$q") | grep -c '^[<>]')
  echo "per-block heap $t: differing lines=$d"
  [[ $d -eq 0 ]] || bad=1
done
exit $bad
