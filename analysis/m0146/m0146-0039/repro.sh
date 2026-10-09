#!/usr/bin/env bash
# M0146-0039 repro: a temp table resurrects as a PERMANENT public table after
# a restart. Throwaway cluster on :5533 via the cgroup wrapper.
set -u
cd "$(git rev-parse --show-toplevel)"
export PATH=$PWD/postgres/local_install/bin:$PATH LD_LIBRARY_PATH=$PWD/postgres/local_install/lib
go build -o tmp/zz-goopg ./cmd/goopg
rm -rf tmp/zz-tt && tmp/zz-goopg init -D tmp/zz-tt >/dev/null 2>&1
start(){ (GOOPG_CG_UNIT=zz-tt scripts/goopg-test-run.sh tmp/zz-goopg start -D tmp/zz-tt --listen 127.0.0.1:5533 > tmp/zz-tt.log 2>&1 &)
  for i in $(seq 1 60); do pg_isready -q -h 127.0.0.1 -p 5533 -U postgres -d postgres && break; sleep 1; done; }
stop(){ tmp/zz-goopg stop -D tmp/zz-tt >/dev/null 2>&1; systemctl --user stop zz-tt.scope 2>/dev/null; sleep 2; }
P="psql -X -h 127.0.0.1 -p 5533 -U postgres -d postgres"
start
$P -c "CREATE TABLE keep (k int PRIMARY KEY)" -c "INSERT INTO keep VALUES (7)" \
   -c "CREATE TEMP TABLE ca (k int)" -c "INSERT INTO ca VALUES (1)" \
   -c "CREATE TEMP TABLE cb (id serial PRIMARY KEY, v int DEFAULT 3 CHECK (v > 0), w text)" \
   -c "CREATE INDEX cb_w ON cb (w)" -c "INSERT INTO cb (w) VALUES ('x')" \
   -c "CREATE TEMP SEQUENCE ts" -c "SELECT nextval('ts')"
stop; start
$P -c "SELECT relname, relpersistence FROM pg_class WHERE relname IN ('ca','cb','cb_pkey','cb_w','cb_id_seq','ts','keep') ORDER BY 1" \
   -c "SELECT * FROM keep" -c "SELECT * FROM ca" \
   -c "CREATE TEMP TABLE ca (k int)" -c "INSERT INTO ca VALUES (2)" -c "SELECT * FROM ca"
stop
