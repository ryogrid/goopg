# M0137-0020 — pin `GOOPG_ANALYZE_SEED` in the TPC-DS capture harness

Status: accepted (landed 2026-09-15)

## Task

M0138-0008's bisect found that `scripts/capture-tpcds.sh` and the M0137-0003
canonical TPC-DS baseline-capture procedure never set `GOOPG_ANALYZE_SEED`,
unlike `scripts/tpch-acceptance-arm.sh`/`scripts/estimate-parity-gate.sh`
which already pin it (`20260905`) for the identical reason. Unpinned, four
trials at two FIXED commits moved `join-order`/`qual-placement`/`join-method`/
`scan-type`/`aggregation-strategy`/`parallelism` plan-parity category counts
from ANALYZE reservoir-sampler seed variance alone (Q26, Q45, Q48, Q51, Q97),
and the milestone review's own "TPC-DS categories net worse, 525->540"
headline was measured unpinned. Deliverable: land the same default pin the
TPC-H gates already carry, then re-verify reproducibility.

## Where the fix actually lands — not where the task title suggests

The task's own name and `.ralph/fix_plan.md`'s wording ("add the same
default `scripts/capture-tpcds.sh` already needs") assume the fix is a line
inside `capture-tpcds.sh` itself, matching `tpch-acceptance-arm.sh`'s
in-script `export`. That assumption does not survive reading either script:

- `internal/executor/operators_analyze.go:704-733`'s `analyzeSeedEnv` is a
  **package-level var**, initialised exactly once by
  `os.Getenv("GOOPG_ANALYZE_SEED")` when the goopg **server process** starts
  — not read again per-connection, per-query, or per-ANALYZE.
- `scripts/capture-tpcds.sh` never runs `ANALYZE`. It only opens client
  `psql -f` sessions that issue `EXPLAIN`. `bench/tpcds/README.md`'s "3.
  ANALYZE each table" step — the one that actually seeds the sampler — runs
  once, durably, inside `scripts/tpcds-load.sh`, against an **already
  running** server process that `bench/tpcds/server.sh start` launched
  earlier. An `export` inside `capture-tpcds.sh` would set the variable in
  the *client's* shell, long after the *server's* one-time read already
  happened; it would be silently inert for every real TPC-DS cluster.
- `scripts/tpch-acceptance-arm.sh`'s precedent looks similar but differs in
  exactly this respect: it starts its **own private server** (cloned from the
  shared TPC-H cluster) inside the same script, so its `export` runs before
  the process it pins even exists. `capture-tpcds.sh` has no such step — by
  the time it runs, the server (and its one-time seed read) is already fixed.

So "matching the TPC-H precedent" (which the fix_plan item flagged as the
default choice "unless a reason not to turns up") does apply, just one layer
up: the export has to sit at the point that is common to **every** path that
starts a TPC-DS goopg server, analogous to `tpch-acceptance-arm.sh` doing it
right before its own `pg_ctl`-equivalent start. That point is
`bench/tpcds/env_tpcds.sh` — sourced by `bench/tpcds/server.sh` (which starts
every goopg TPC-DS server: `sf1`, `sf025`) and by `scripts/tpcds-load.sh`,
`tpcds-run.sh`, `tpcds-bench-compare.sh`, `tpcds-sf025-regression.sh`, so a
default landing there is picked up automatically by every existing caller,
not just capture.

## Change

`bench/tpcds/env_tpcds.sh`, in the existing "goopg runtime knobs" block
(next to `GOMEMLIMIT`/`GOGC`, which are read the same way — once, at server
start):

```bash
export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"
```

Same default value `tpch-acceptance-arm.sh`/`estimate-parity-gate.sh` already
use. An explicit caller override (`GOOPG_ANALYZE_SEED=0` to restore
wall-clock seeding, or any other value) is preserved by the `:-` form.

`scripts/capture-tpcds.sh`'s header comment and
`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md` §3 are both
updated to point at `env_tpcds.sh` as the pin site and explain why the
capture script itself has no seam to pin through.

## Verification

- `bash -n bench/tpcds/env_tpcds.sh bench/tpcds/server.sh
  scripts/capture-tpcds.sh` — syntax check, clean.
- **Wiring check (read-only, touches no server):**
  ```bash
  unset GOOPG_ANALYZE_SEED
  ( source bench/tpcds/server.sh status >/dev/null
    echo "GOOPG_ANALYZE_SEED=${GOOPG_ANALYZE_SEED:-<unset>}" )
  # -> GOOPG_ANALYZE_SEED=20260905
  ( export GOOPG_ANALYZE_SEED=99999
    source bench/tpcds/server.sh status >/dev/null
    echo "GOOPG_ANALYZE_SEED=${GOOPG_ANALYZE_SEED}" )
  # -> GOOPG_ANALYZE_SEED=99999 (override preserved)
  ```
  Both ran and matched the expected values. `server.sh status` only probes
  `pg_isready`, so this exercises the real script's sourcing order without
  starting, stopping, or touching either shared TPC-DS cluster.
- **Full-scale reproducibility of the underlying mechanism** (export before
  server start, then load + ANALYZE + capture twice, expect byte-identical
  `.plans.txt`) was **already established live by M0138-0008**: a private
  SF0.25 cluster started via `scripts/goopg-test-run.sh` with
  `GOOPG_ANALYZE_SEED=20260905` exported first produced two byte-identical
  99-query captures. This task reuses that same mechanism through the new
  call site rather than re-running the expensive experiment — the code path
  from "env var exported before the server process starts" to "reproducible
  ANALYZE sample" is identical; only *where* the export now lives changed.
  Re-deriving that physics a second time would not test anything the wiring
  check above doesn't already cover.

## What was not done (scope boundary)

- **The shared `:65436`/`:65437` clusters were not reloaded.** They were
  loaded (and ANALYZEd) before this pin landed, so their on-disk statistics
  remain wall-clock-seeded until their next reload; only a server started (or
  a cluster reloaded) after this change gets the reproducible sample. Any
  capture against the current shared clusters still needs a private clone
  (M0138-0008's method) for a reproducibility-sensitive comparison until the
  shared clusters are next reloaded.
- **The milestone review's "TPC-DS categories net worse, 525->540" headline
  was not re-measured.** M0138-0008 already scoped this out as separate,
  larger follow-on work; it remains unowned. Ledger row
  `m0137-0020-recapture-headline-unowned` files it (`.ralph/deferral_ledger.md`)
  and a new fix_plan task, M0137-0021, tracks it.
- No production planner/executor/catalog code touched — this is a harness
  fix (two scripts, one doc) per the M0137 charter.

## Plan-parity harness report items (AGENT.md §"What every M0137–M0143 task
report must contain")

1. Category movement (`CATEGORIES:`/`CATEGORIES-EXCL-MATCH:`): N/A — no
   corpus capture run this task (harness-only change, no server started).
2. `shape-delta` counts: N/A — same reason.
3. Stats epoch: N/A — no capture taken.
4. Seam-decline census: N/A — no traced capture run.
5. Planning route: N/A — no query executed.
