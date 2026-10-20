#!/usr/bin/env bash
# one round: 8 concurrent pgbench clients, each 50 x the CTE query; prints abort count
B=/home/ryo/work/goopg/goopg/postgres/local_install/bin; O=/home/ryo/work/goopg/goopg/tmp/p0e8/r; mkdir -p $O; rm -f $O/*
for i in $(seq 8); do $B/pgbench -n -h 127.0.0.1 -p 5533 -U postgres -c 1 -t ${2:-50} -f ${1:-/home/ryo/work/goopg/goopg/tmp/p0e8/mini.sql} postgres > $O/$i.log 2>&1 & done
wait
echo "aborts=$(grep -l 'does not exist' $O/*.log | wc -l)"
