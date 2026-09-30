#!/usr/bin/env bash
# M0146-0039 repro: a temp table resurrects as a PERMANENT public table after
# a restart. Throwaway cluster on :5533 via the cgroup wrapper.
set -u
cd "$(git rev-parse --show-toplevel)"
export PATH=$PWD/postgres/local_install/bin:$PATH
go build -o tmp/zz-goopg ./cmd/goopg
rm -rf tmp/zz-tt && tmp/zz-goopg init -D tmp/zz-tt >/dev/null
start(){ (GOOPG_CG_UNIT=zz-tt scripts/goopg-test-run.sh tmp/zz-goopg start -D tmp/zz-tt --listen 127.0.0.1:5533 > tmp/zz-tt.log 2>&1 &)
  for i in $(seq 1 60); do pg_isready -q -h 127.0.0.1 -p 5533 -U postgres -d postgres && break; sleep 1; done; }
stop(){ tmp/zz-goopg stop -D tmp/zz-tt >/dev/null 2>&1; systemctl --user stop zz-tt.scope 2>/dev/null; sleep 2; }
P="psql -X -h 127.0.0.1 -p 5533 -U postgres -d postgres"
start
$P -c "CREATE TEMP TABLE ca (k int)" -c "INSERT INTO ca VALUES (1)"
stop; start
$P -c "SELECT relname, relnamespace::regnamespace, relpersistence FROM pg_class WHERE relname='ca'" \
   -c "SELECT * FROM ca" -c "CREATE TEMP TABLE ca (k int)"
stop
