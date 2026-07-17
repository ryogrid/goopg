# 01 — Where the Time Goes

Status: draft. Part of [test-gate-speedups](README.md).

This document is the measured baseline every other document in the bundle
argues from. All numbers are reproducible with the commands shown.

## 1. The gate inventory and what each costs

| Gate | Trigger | What it runs | Measured wall-clock |
|------|---------|--------------|---------------------|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | manual, once per change (mandatory) | `go test -timeout 10m` over all packages minus testport/server/cluster/bench (`EXCLUDE` at `scripts/ralph-precommit-test.sh:65`) | ~310 s warm cache; 2229–2412 s cold; **10 466 s** cold under nightly co-load (2026-07-11 row) |
| pgbench smoke (`.githooks/pre-commit`, every commit) | every `git commit` | build → `goopg init` → server start → `pgbench -i` (scale 1) → 3 × 30 s workloads (std / `-N` / `-S`) | ~2–3 min, of which 90 s is the fixed measurement window |
| `make race-gate` | concurrency-touching changes; nightly | `go test -race -timeout 15m` over the same package set | ~440 s warm; 3577–4474 s cold |
| `internal/testport` suite | oracle/regress/isolation work; nightly | 351 test functions (325 in the top-level package, which boot one or more goopg servers each — 514 `newCluster`/`mustInitStart` call sites; 26 in `testport/framework` boot nothing) | ~1303–1411 s warm; **9468–11 302 s** cold |
| `scripts/tpch-spotcheck.sh` | executor/planner/codec changes | fresh capped server restart + Q12/Q13 canonical row counts | ~1 min |
| nightly TPC-H sweep | nightly | 22 queries on the SF-scale bench dir | ~2000 s, **cache-insensitive** (live-server CPU work; Q5 506 s, Q9 302 s, Q21 294 s, Q7 164 s per `ci/logs/latest/summary.md`) |
| nightly pgbench | nightly | SF 50, c=100, j=20, 3 × 180 s | 575–980 s |

Source for the nightly stage durations — last 7 runs of
`ci/logs/history.jsonl` (seconds):

```bash
tail -7 ci/logs/history.jsonl | python3 -c '
import json,sys
for l in sys.stdin:
    d=json.loads(l); print(d["run_id"], d.get("durations"))'
```

| run | units | race | testport | pgbench | tpch |
|-----|-------|------|----------|---------|------|
| 20260711 | 10 466 | 4 474 | 11 302 | 978 | 2 005 |
| 20260712 | 2 412 | 3 577 | 9 468 | 804 | 1 947 |
| 20260713 | 2 375 | 3 905 | 9 511 | 724 | 1 973 |
| 20260714 | 2 395 | 3 882 | 2 157 | 624 | 1 972 |
| 20260715 | 2 229 | 3 771 | 1 411 | 585 | 2 065 |
| 20260716 | **307** | **442** | **1 303** | 575 | 2 040 |
| 20260717 | **310** | **436** | **1 355** | 592 | 2 026 |

## 2. Decomposition: the four dominant cost sources

### 2.1 The Go build/test cache is the single biggest lever — and it is unmanaged

The 2026-07-16/17 rows above are warm-cache runs: units drops 2412 s → 307 s
(7.8×), race 3577 s → 442 s (8.1×) with **zero configuration** — Go's test
result cache simply hit. Nothing in any gate passes `-count=1` (which would
defeat the cache), but nothing *protects* this behavior either: it is an
accident, not policy. [05](05-gate-scoping-and-cache-policy.md) turns it into
policy.

The cache also explains the units-gate variance developers experience: a
branch switch, a toolchain change, or any edit to a widely-imported package
invalidates most of it, and the next `units` run pays the full ~40 min cold
cost — or ~2.9 h when a cold run coincides with nightly co-load (the
10 466 s 2026-07-11 row).

### 2.2 Every test cluster pays a durability fsync it does not need

`goopg init` ends with a recursive fsync of the data directory
(`internal/initdb/initdb.go:1285-1288`, mirroring upstream `initdb.c`
`sync_pgdata`), skippable via `Options.NoSync` / the `--no-sync` CLI flag
(`cmd/goopg/main.go:239`). Measured on this host (see
[02 §5](02-durability-off-for-test-servers.md) for the probe protocol):

| command | wall |
|---------|------|
| `./bin/goopg init -D <disk>` | 0.77 s |
| `./bin/goopg init -D <disk> --no-sync` | 0.14 s |
| `./bin/goopg init -D /dev/shm/...` | 0.09 s |

Nobody in the gate path uses `--no-sync` for goopg clusters:

- `scripts/ralph-precommit-test.sh:208` — plain `./bin/goopg init -D "$DATADIR"`.
- `internal/testutil/cluster` `(*Cluster).Init` (`cluster.go:148-159`) — no
  `--no-sync`, and no testport caller passes it via `Options.InitArgs`
  (`cluster.go:46`), which already exists as a seam.
- Of the 178 `Init(Options{` call sites in `internal/initdb/*_test.go`
  (173 single-line, 5 multi-line openers), only 9 set `NoSync: true` inline
  (13 `NoSync: true` lines in the package overall) — ~165 bootstraps pay the
  ~0.6 s fsync, ≈100 s of the package's ~223 s.

(The `--no-sync` occurrences in `internal/testport` are pg_dump /
pg_basebackup *client* flags, and `scripts/pg-oracle-diff.sh:134` applies it
only to the **vanilla PG** oracle side — upstream's harness already does what
goopg's own harness doesn't.)

### 2.3 `internal/testport` boots a full server per test

351 test functions (325 in the top-level package; the other 26 live in
`testport/framework` and boot nothing). The shared glue `newCluster`
(`internal/testport/tap_port_test.go:191`) creates a fresh
`t.TempDir()`-backed data dir and `mustInitStart` (`:205`) runs a full
`goopg init` + subprocess `goopg start` (up to 20 s readiness wait) + stop
(up to 20 s); most tests boot one cluster this way and some boot several —
514 `newCluster`/`mustInitStart` call sites across the package. Only 3 of
the 351 call `t.Parallel()`. Cold, the package costs 9468–11 302 s; even
warm it is the longest non-TPC-H stage (~1300 s). Every per-init and
per-I/O saving in docs 02/03 multiplies by the ~500 boot sites here; the
template cache of doc 04 removes the bootstrap cost entirely.

### 2.4 The big packages are effectively serial

`t.Parallel()` census (test functions / `t.Parallel()` calls):

| package | tests | parallel |
|---------|-------|----------|
| internal/executor | 1339 | 0 |
| internal/wal | 744 | 158 |
| internal/initdb | 625 | 3 |
| internal/testport | 351 | 3 |
| internal/server | 261 | 9 |
| internal/catalog | 256 | 0 |
| internal/storage | 182 | 0 |

Within a package, Go runs tests sequentially unless they opt in. For
I/O-wait-heavy packages (initdb: fsync waits; testport: server startup waits)
serial execution leaves the CPU idle most of the time. Note the nightly
already caps *cross-package* parallelism (`GOFLAGS=-p=4`,
`ci/batch/stages/stage-units.sh:30`) for memory safety — intra-package
`t.Parallel()` needs the same discipline ([04 §2](04-parallelism-and-bootstrap-caching.md)).

## 3. What is NOT addressable by I/O levers

- **TPC-H sweep (~2000 s)**: live-server CPU work on a persistent 2.2 GB data
  dir; cache-insensitive and dominated by four queries (Q5/Q9/Q21/Q7). Speeding
  it up is planner/executor performance work, out of scope here.
- **The 3 × 30 s pgbench smoke windows (90 s)** and the nightly 3 × 180 s
  windows: these ARE the measurement; shortening them trades away the
  perf-tripwire signal ([05 §3](05-gate-scoping-and-cache-policy.md)).
- **`tpch-spotcheck.sh` (~1 min)**: already near-minimal (server restart
  dominates).

## 4. Ranked opportunities (elaborated in docs 02–05)

1. **Protect the test cache as policy** — free; preserves the existing 7–8×
   ([05 §1](05-gate-scoping-and-cache-policy.md)).
2. **`--no-sync` for every throwaway cluster init** — zero prod code, three
   small diffs ([02 Part A](02-durability-off-for-test-servers.md)).
3. **tmpfs data dirs** for initdb/testport/server tests and the smoke gate —
   11× measured on the initdb probe subset ([03](03-tmpfs-data-dirs.md)).
4. **initdb template caching** — removes ~516 redundant bootstraps per full
   run ([04 §1](04-parallelism-and-bootstrap-caching.md)).
5. **`t.Parallel()` pilots** in initdb → executor → catalog
   ([04 §2](04-parallelism-and-bootstrap-caching.md)).
6. **`fsync` GUC off for test servers** — the remaining runtime fsyncs
   ([02 Part B](02-durability-off-for-test-servers.md)).
7. **Affected-package selection** — biggest per-commit win, highest risk,
   staged last ([05 §2](05-gate-scoping-and-cache-policy.md)).

## Appendix — draft gate-timing reporter (proposal, not shipped)

A tiny read-only reporter so before/after evidence for the rollout
([06 Part B](06-prompt-changes-and-rollout.md)) can be produced by anyone:

```bash
#!/usr/bin/env bash
# gate-timing-report.sh — per-stage nightly durations from ci/logs/history.jsonl
# PROPOSED (lives only in this design doc until the rollout loop lands it).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
N="${1:-14}"
tail -n "$N" ci/logs/history.jsonl | python3 -c '
import json, sys
rows = [json.loads(l) for l in sys.stdin]
stages = ["preflight", "units", "race", "testport", "pgbench", "tpch"]
print("run_id           " + "".join("{:>10}".format(s) for s in stages))
for r in rows:
    d = r.get("durations", {})
    print("{:<17}".format(r.get("run_id", "?"))
          + "".join("{:>10}".format(d.get(s, "-")) for s in stages))
'
```

(No f-strings with escaped quotes — the program sits inside bash single
quotes, so `.format()` is used throughout; verified runnable against
`ci/logs/history.jsonl` on this host.)
