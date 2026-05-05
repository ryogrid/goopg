# hammerdb_load — standalone TPC-H ORDERS/LINEITEM batched-INSERT loader

This is the M0032-0005 reproducer for the
"HammerDB SF=1 load drops at ~430 k orders" symptom captured in
`analysis/tpch-hammerdb-run-002.md`. It bypasses HammerDB's TCL
toolchain (`HammerDB/src/postgresql/pgolap.tcl`) and reproduces the
same wire shape — batched multi-row `INSERT ... VALUES (…),(…),…`
with `--commit-interval` orders per `COMMIT` — directly from a Go
program.

The values are synthetic (the goal is INSERT throughput, not
TPC-H result correctness; HammerDB's dbgen remains the
authoritative source for that). The schema matches goopg's
`internal/testutil/tpch/tpch.go::tableDefs()` so the loaded data
plays nicely with the existing TPC-H query suite.

## Build

```
go build -o /tmp/hammerdb_load ./bench/tpch/cmd/hammerdb_load
```

## Use

Smoke (10 k orders, ~3 minutes against the default goopg
configuration):

```
/tmp/hammerdb_load \
    --addr 127.0.0.1:65433 \
    --user postgres --db postgres \
    --batch-rows 10 --commit-interval 100 \
    --limit-orders 10000
```

Full SF=1 (1.5 M orders, ~6 M lineitems):

```
/tmp/hammerdb_load \
    --addr 127.0.0.1:65433 \
    --user postgres --db postgres \
    --scale 1 --batch-rows 10 --commit-interval 100
```

The loader prints a progress line every 10 000 orders with the
elapsed seconds and the running orders/s rate, plus a summary
line at the end. Slow runs that don't reach completion within
the M0032-0005 acceptance criterion (full SF=1 without the
loader connection dropping) feed `analysis/tpch-hammerdb-run-004.md`.

## Profile capture

`bench/tpch/profile_load.sh` wraps a smoke run with pprof
captures from goopg's `-pprof-listen` endpoint. See that script
for the exact CPU/heap profiles M0032-0005's slice-2 fixes use
to pick the top 2-3 hotspots to attack.
