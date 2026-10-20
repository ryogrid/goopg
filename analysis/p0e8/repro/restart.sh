#!/usr/bin/env bash
R=/home/ryo/work/goopg/goopg; D=$R/tmp/p0e8/data; B=$R/postgres/local_install/bin
timeout 60 ${GOOPG_BIN:-$R/tmp/p0e8/goopg} stop -D $D >/dev/null 2>&1; systemctl --user stop goopg-p0e8.scope >/dev/null 2>&1; sleep 1
systemctl --user reset-failed goopg-p0e8.scope >/dev/null 2>&1
GOOPG_CG_UNIT=goopg-p0e8 setsid nohup $R/scripts/goopg-test-run.sh ${GOOPG_BIN:-$R/tmp/p0e8/goopg} start -D $D --listen 127.0.0.1:5533 > $R/tmp/p0e8/server.log 2>&1 < /dev/null &
for i in $(seq 60); do $B/pg_isready -h 127.0.0.1 -p 5533 -U postgres -q && exit 0; sleep 1; done; echo NOT READY; exit 1
