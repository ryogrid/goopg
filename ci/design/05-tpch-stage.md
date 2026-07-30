# 05 — TPC-H Stage (S2)

The one stage with real OOM potential and unbounded-runtime potential
(planning failures historically produced 600 s+ single queries — M0076/M0077).
Hence: runs solo, memory-capped, and under a **2-hour total wall-clock limit**
(requirement 8).

## A. Preconditions & setup

**Isolation from the loop's `tpch-spotcheck.sh` (requirement):** the loop's
spotcheck unconditionally `goopg stop`s whatever server runs on the canonical
bench data dir (`bench/tpch/runtime_goopg/data`, port 65433) — so the batch
must NOT hold that dir/port for a multi-hour stage, and the two must be able
to run concurrently (spotcheck is light: ~3–4 min, small footprint). The
batch therefore runs on a **snapshot copy** of the data dir at its own port:

1. **Data check** — canonical dir exists and ≥ 100 MB (mirrors
   `TPCH_SPOTCHECK_MIN_MB`); else stage = `skip(no-data)`, exit 0.
   The nightly never (re)loads data — the HammerDB load is hours-long and a
   reload would invalidate the Q13 row-count pin (Q13 is load-dependent;
   re-pinned to 33 on the 2026-06-13 dataset).
2. **Snapshot copy** — the copy is only consistent while no server writes the
   source, so: wait for 65433 to be server-free (≤ `NIGHTLY_PORT_WAIT`),
   `cp -a` the dir to `tmp/goopg-nightly-tpch-data` (~2.2 GB, tens of
   seconds), then verify 65433 stayed free; a server appearing mid-copy ⇒
   redo (3 attempts, then `skip(port-busy)`). After the copy the canonical
   dir/port are released — a loop spotcheck can run at any time during the
   rest of the stage.
3. **Fresh capped server start ON THE COPY** at
   `${NIGHTLY_TPCH_PORT:-65434}` (batch-reserved lane) — via
   `scripts/goopg-test-run.sh` with `GOOPG_CG_UNIT=goopg-nightly-tpch`,
   `GOOPG_MEM_HIGH=10G`, `GOOPG_MEM_MAX=12G`, `GOOPG_MEM_SWAP_MAX=0`,
   `GOMEMLIMIT=9GiB`; readiness wait ≤ 120 s (spotcheck's
   `TPCH_SPOTCHECK_READY_TIMEOUT` precedent — SLRU backfill can make startup
   slow). The copy IS the fresh state (never touched by a server since the
   snapshot), satisfying the fresh-restart rule; the clone is deleted in the
   stage's cleanup trap.
4. Ensure the runner binary: `go build -o tmp/tpch-runner ./cmd/tpch-runner`
   (cheap, part of the stage).
5. **Connection identity after a fresh restart:** goopg roles/databases are
   in-memory only — the `tpch` role/DB do NOT survive the restart, while the
   loaded tables persist in the **`postgres` database under the superuser**.
   `tpch-runner`'s defaults (`-db tpch -user tpch -password tpch`) would
   therefore fail with "role/database does not exist" on every query. The
   stage must use the same fallback `scripts/tpch-spotcheck.sh` implements
   (superuser @ `postgres`): every runner invocation in this stage carries
   `-db postgres -user "$SUPERUSER"` (resolved exactly as the spotcheck
   script resolves it).

Stage-local `trap EXIT`: stop the server (`postmaster.pid` PID →
`goopg stop -D` fallback), `systemctl --user stop goopg-nightly-tpch.scope`.

## B. Step 1 — spotcheck tripwire

Run Q12 and Q13 **directly against the stage's clone server (65434)** and
compare with `bench/tpch/spotcheck_expected.env` (**Q12=2 structural, Q13=33
pinned**) — the same semantics as `scripts/tpch-spotcheck.sh`, without
invoking it. Invoking the script here would be wrong twice over: it runs its
own fresh server on the CANONICAL dir/port (65433) — re-entering the lane the
clone strategy exists to leave to the loop — and it would validate that
server, not the clone the sweep actually runs on. The stage reuses the
script's expected-values file and its superuser@`postgres` connection
fallback (§A.5), not its server lifecycle.

- **Mismatch ⇒ the stage is FAILED immediately and the 22-query sweep is
  skipped.** Rationale: the spotcheck failing means correctness is already
  broken (the known catastrophic signature is `Q12=0/Q13=2`); burning up to
  2 h on a known-bad tree wastes the window and the log signal. The summary
  marks `tpch: fail(spotcheck)` with both counts.
- Small Q13 drift after a *deliberate* data reload is a re-pin, not a
  regression — but since the nightly never reloads, any drift here is real.

## C. Step 2 — EXPLAIN capture (plans are part of the record, requirement 5)

Before executing queries, capture all 22 plans:

```bash
tmp/tpch-runner -port 65434 -db postgres -user "$SUPERUSER" \
  -explain -per-query-timeout 60s \
  | tee "$RUN_DIR/tpch/explain-run.log"
# split per query into tpch/explain/qNN.txt
```

- `-explain` issues `EXPLAIN <query>` and prints the full plan text per query
  (`cmd/tpch-runner/main.go`); EXPLAIN is fast (no execution), so 60 s/query
  is generous. An EXPLAIN *error* for any query is gating (a query that can't
  plan can't run).
- Optionally also run plan-gate's structural diff while the server is up —
  its output lands in `tpch/plan-gate.log` as the `plan-drift` informational
  signal (doc 04 §C); SKIP(0) semantics make this safe when the snapshot is
  missing. **Snapshot-pinning gotcha:** `make plan-gate` picks its baseline
  by `ls -t plan_snapshots/*.txt | head -1`, and on this checkout
  `m0077-final.txt` and `m0076-baseline-ffc3429.txt` have *identical mtimes*
  — the tie resolves to `m0076-baseline`, i.e. the PRE-M0077 plans, which
  would guarantee spurious drift noise every night. The stage must pin the
  intended baseline explicitly (`make plan-snapshot-diff LABEL=m0077-final`,
  or `touch plan_snapshots/m0077-final.txt` once at implementation time).
- Capturing plans **before** the sweep means that even a sweep that dies at
  q1 leaves the full plan record — the most valuable regression forensics.

## D. Step 3 — the 2-hour-bounded 22-query sweep

**Scope of the 2-hour limit (normative):** the 7200 s budget bounds the
22-query sweep itself (requirement: "run the 22 queries with a 2 h total
limit"). Steps 1–2 sit outside it but carry their own small bounds — server
readiness ≤ 120 s, spotcheck ≤ ~5 min, EXPLAIN ≤ 22 × 60 s — so the whole
stage is ≤ ~2 h 35 min worst case (doc 02 §E quotes it this way).

Two nested limits:

**Outer clamp (absolute):** the whole sweep runs under
`timeout --signal=INT 7200s`, with the runner's `-signal-file` as the
cooperative escape hatch: on INT the orchestrator also touches the signal
file so the current query is cancelled cleanly (CancelRequest → SQLSTATE
57014) rather than the connection being torn mid-protocol.

**Inner budget loop (per query):** queries are driven **one at a time** so
the budget can adapt:

```
BUDGET = 7200s;  RESERVE = 120s          # reserve: checkpoint + shutdown + bookkeeping
order  = canonical power-test order (14,2,9,20,6,17,18,8,21,13,3,22,16,4,11,15,1,10,19,5,7,12)
for q in order:
    remaining = BUDGET - elapsed_so_far
    if remaining <= RESERVE:
        mark q..rest = not-run(budget); break
    per_q = min(1200s, remaining - RESERVE)
    tmp/tpch-runner -port 65434 -db postgres -user "$SUPERUSER" \
        -queries q -per-query-timeout ${per_q}s
    parse elapsed/rows/status → tpch/timings.csv; progress.log line
```

- `min(1200 s, …)` caps any single query at 20 min — 4× the slowest baseline
  query (Q21 = 295 s), so legitimate co-load slowdown passes while a
  planning-blowup query is cut without eating the whole window.
- A per-query timeout ⇒ the runner cancels (57014) and the loop **continues
  with the next query** — one bad query still yields 21 data points.
- Timeouts and `not-run(budget)` are both `perf-drastic` events (doc 04 §C)
  ⇒ the run fails, but with a complete-as-possible record.
- Canonical order matches the HammerDB power test / the 20260526 baseline log
  so per-position comparisons line up.

## E. Step 4 — comparison & verdict

| Check | Reference | Gating? |
|-------|-----------|---------|
| Q12/Q13 row counts | `bench/tpch/spotcheck_expected.env` (Q12=2, Q13=33) | **yes** (step 1) |
| per-query row counts | anchors from `analysis/tpch/m0093-q1-q22-regression-sweep.md` (Q1=4, Q3=11 686, Q5=5, Q9=175, Q10=20 412, Q11=791, Q13→33 current, Q16=18 360, Q18=9, Q21=397, Q22=7; full table transcribed into `ci/batch/` as `tpch-row-anchors.csv` at implementation) | **yes** — anchor mismatch = regression |
| query error / EXPLAIN error | — | **yes** |
| per-query timeout, budget exhaustion | doc-05 budget | **yes** (`perf-drastic`) |
| total elapsed | 20260526 power test: total 1469 s, geomean 36.30 s | only if > 2× total (doc 04) |
| per-query elapsed drift | 20260526 per-query column | **no** — recorded in `summary.md` table only |
| plan shape | `plan_snapshots/m0077-final.txt`, explicitly pinned (§C mtime-tie gotcha) | **no** — `plan-drift` note |

Row-anchor caveat to encode in the comparison tool: anchors marked
load-dependent (Q13-style `GROUP BY` over random distributions) must be tied
to the *current dataset pin*, i.e. the anchors CSV carries a
`dataset=20260613` column and the batch refuses to compare anchors whose
dataset tag doesn't match the data dir's recorded load id (a
`bench/tpch/runtime_goopg/DATASET` marker file written at load time —
implementation detail; absent marker ⇒ compare structural anchors only and
note it).

## F. Failure modes rehearsed

| Event | Outcome |
|-------|---------|
| server OOM-killed by its own cgroup (12G MemoryMax) | current query errors; runner marks it, sweep aborts (connection gone) ⇒ stage `fail`; scope log names the kill — clearly distinguished from host OOM |
| host memory pressure → Linux OOM killer takes the runner or server (the batch is OUTSIDE `mem_guard.py`'s watch tree — doc 03 §C) | signal 9, no panic ⇒ `resource-kill`, run `inconclusive`; forensics via `dmesg`/`journalctl` (doc 02/03) |
| startup exceeds ready-timeout | stage `fail(startup)`; fsync-storm history says check SLRU backfill first |
| canonical 65433 busy through every copy attempt | `skip(port-busy)` — never kill the holder; the copy window is the only 65433 touchpoint |
| data dir absent/small | `skip(no-data)` |
| **server will not exit after the sweep** (a backend leaked by a per-query TIMEOUT keeps graceful shutdown from ever returning) | `cleanup` escalates `graceful 120s → -mode immediate 60s → SIGTERM 30s → SIGKILL` via `stop_goopg_server` (lib/common.sh), capturing a goroutine dump to `tpch/server-goroutines.txt` before the first escalation, and reports the rung through `progress`. Was an **untimed `wait`** until 2026-07-29, when it held a 16-core host for 6h45m after the sweep had already finished — with no error in any log. See [root-0037](../../docs/design/root-0037-nightly-server-shutdown-ladder.md); the engine leak itself is still open. |
