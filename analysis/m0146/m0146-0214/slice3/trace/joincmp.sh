#!/usr/bin/env bash
# joincmp.sh <eval-index>: trace one GEQO evaluation's join rels on PG (5547) and goopg (5599), interleaved.
set -u
R=/home/ryo/work/goopg/goopg; E=$1; P=$R/tmp/pgdbg/inst/bin
$P/pg_ctl -D $R/tmp/m21-refclone stop -m fast >/dev/null 2>&1
GEQO_TRACE_EVAL=$E $P/pg_ctl -D $R/tmp/m21-refclone -l $R/tmp/m0214s3/pg.log -o "-p 5547 -c autovacuum=off -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp" start >/dev/null 2>&1; sleep 2
L=$(wc -l < $R/tmp/m0214s3/pg.log); $P/psql -X -h 127.0.0.1 -p 5547 -d tpcds025 -f $R/tmp/m0214s3/q64.sql >/dev/null 2>&1
tail -n +$L $R/tmp/m0214s3/pg.log | grep -a 'GEQOJOIN' | sed 's/^.*LOG: *//; s/type=[0-9]* //' > $R/tmp/m0214s3/pg-join$E.txt
timeout 60 $R/tmp/m0214s3/goopg stop -D $R/tmp/m0214s3/data >/dev/null 2>&1; systemctl --user stop goopg-m0214s3.scope 2>/dev/null; sleep 1
GOOPG_GEQO_TRACE_EVAL=$E $R/tmp/m0214s3/clone.sh >/dev/null
L=$(wc -l < $R/tmp/m0214s3/server.log); $R/postgres/local_install/bin/psql -X -h 127.0.0.1 -p 5599 -U postgres -d postgres -f $R/tmp/m0214s3/q64.sql >/dev/null 2>&1
tail -n +$L $R/tmp/m0214s3/server.log | grep -a '^GEQOJOIN' | sed 's/kind=[0-9]* //' > $R/tmp/m0214s3/go-join$E.txt
paste -d'\n' <(awk '{print "PG "$0}' $R/tmp/m0214s3/pg-join$E.txt) <(awk '{print "GO "$0}' $R/tmp/m0214s3/go-join$E.txt) | awk 'NR%2==1{p=$0;next}{split(p,a," ");split($0,b," "); if (a[5]!=b[5] || a[4]!=b[4] || a[3]!=b[3]) {print p; print $0; n++; if(n>=2) exit}}'
