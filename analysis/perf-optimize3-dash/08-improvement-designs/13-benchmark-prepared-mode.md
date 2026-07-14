# 08-13 — Benchmark methodology: prepared-statement mode

status: methodology · date: 2026-07-14 · base: `a640d2b0` → [README](README.md)

## 1. The change

Both perf harness scripts now run pgbench in **prepared-statement mode by
default** (`-M prepared`, the extended query protocol with a server-side
prepared statement per SQL text), via a `QMODE` env knob mirroring the existing
`SCALE` knob:

- `analysis/perf-optimize3/scripts/run_rw50.sh` — `QMODE="${QMODE:-prepared}"`
  (line ~41); the shared `run_workload()` pgbench invocation passes `-M
  "$QMODE"`, covering all four runs (goopg/PG × `-S`/`-N`).
- `analysis/perf-optimize3/scripts/aux2_fsync_probe.sh` — same `QMODE` knob; both
  pgbench lines (goopg and PG `-N` probes) pass `-M "$QMODE"`.

Default is `prepared`; `QMODE=simple` reproduces the 06/07 measurements, so the
parse/plan cost can be attributed by difference. Scale factor stays 100.
Everything else (c=50/j=50, T=120, `-P 30 -r`, ports, conf deltas) is unchanged.

Rationale: 06-03 identified that ~20 % of `-S` CPU is per-query parse+plan that
prepared statements remove; `-M prepared` is how you measure the engine at its
plan-cache ceiling instead of paying re-parse on every point query.

## 2. The interaction that makes this non-trivial

goopg's extended-query protocol **auto-commits one transaction per `Execute`**
(doc 09; `internal/server/dispatch_extended.go:30`, `TxnMgr.Begin` at :119;
BEGIN/COMMIT are accepted-but-ignored tags). So `-M prepared` does not merely
strip parse/plan on the write path — it **fractures** the `-N` transaction
(`BEGIN;UPDATE;SELECT;INSERT;END`) into separately-committed statements. This is
directly visible in the validation run `prep100_a640d2b0` (this bundle's grounding
measurement):

### `-N` per-statement latency (scale 100, `-M prepared`)

| statement | goopg simple (06) | **goopg prepared** | **PG prepared** |
|---|---:|---:|---:|
| `BEGIN` | 0.229 | 0.219 (no-op tag) | 0.078 |
| `UPDATE` | 1.022 | **3.283** (own commit+fsync) | 0.121 |
| `SELECT` | 0.265 | 0.328 | 0.127 |
| `INSERT` | 0.279 | **3.331** (own commit+fsync) | 0.102 |
| `END` | 3.263 | **0.247** (no-op tag) | 2.798 (the commit) |
| **TPS** | 9,898 | **6,749** | 15,522 |

On goopg the commit cost moved off `END` onto each write statement (two fsyncs
per transaction instead of one) → `-N` TPS *drops* 9,898 → 6,749. On PG the
commit stays on `END` (one fsync/txn), because PG models the explicit block. So
until **doc 09** lands, goopg's `-N -M prepared` numbers measure auto-commit-
per-statement grouping, **not** the prepared write ceiling — they are labeled as
such wherever cited (README X5).

### `-S` (read) — the prepared ceiling, and the goopg gap it exposes

| | goopg | PG | gap |
|---|---:|---:|---:|
| simple (06) | 89,955 | 182,384 | 2.03× |
| **prepared (`prep100`)** | 84,739 | **281,941** | **3.33×** |

PG's read throughput jumps +55 % under prepared mode (parse/plan removed);
goopg's does **not** move (slightly down, within variance). The extended-
protocol per-`Execute` overhead — the auto-begin/commit transaction machinery
(doc 09) plus per-message handling — eats the parse/plan saving that should have
materialized. This widened read gap is the concrete, measured case for the
read-path bundle: **doc 09** (remove the per-`Execute` transaction overhead),
then **docs 08/10/11/12** (operator reuse, force-GC gate, protocol corking,
allocation volume) to actually reach PG's 281,941 ceiling.

The `-S` gap is *valid immediately* (read has no transaction-grouping
confound); the `-N` gap is only valid after doc 09.

## 3. Validation-run notes (`prep100_a640d2b0`)

- `-M prepared` runs against goopg's extended protocol and produces a coherent
  per-statement breakdown: the three write/read workloads (goopg `-N`, PG `-N`,
  PG `-S`) completed with **0 failed transactions**, and the goopg `-S` run
  processed 10,168,345 transactions — so the harness change itself is sound.
- **A real goopg defect surfaced, disclosed here honestly:** the goopg `-S` run
  did **not** finish — clients 24, 35, and 48 aborted mid-run with
  `ERROR: mvcc: unknown transaction`, which triggered `pgbench: error: Run was
  aborted; the above results are incomplete` at ~90 s of the 120 s. That string
  is a goopg engine error (`ErrUnknownTransaction`,
  `internal/mvcc/manager.go:23`), returned over the wire — **not** a benign
  client teardown. It is consistent with the proc-slot hazard doc 09 §I3 itself
  flags: the extended path's autocommit uses an **offset** proc slot
  (`autoCommitProcNum = (procNum + halfSize) % ConnSlotCount`,
  `dispatch_extended.go:118`), and under 50 sustained prepared-mode read clients
  that offset scheme appears to collide/wrap and lose a transaction. This is
  additional, independent evidence for prioritizing **doc 09** (which reworks the
  extended-path transaction model and its proc-slot discipline) — the bug lives
  in exactly the machinery doc 09 replaces.
- The reported `-S` TPS (84,739) is still representative for the gap discussion:
  the run held ~80–86 k TPS across its three progress ticks
  (79,669.9 / 86,168.1 / 84,141.2) before the abort, matching the simple-mode
  89,955 — so the 3.33× read gap stands. But a clean full-120 s `-S` figure
  requires the doc-09 fix (or a re-run that dodges the collision); tracked as
  O-BM-1.

## 4. How the bundle uses prepared mode as its acceptance gate

Once **doc 09** lands, `-M prepared` becomes the PG-faithful measurement for
every subsequent doc:

- Write path (docs 01–06): one commit per `-N` transaction restored → C5's
  group-commit improvement (doc 01) is measurable, and the write ceiling is
  PG's 15,522, not the fractured 6,749.
- Read path (docs 08/10/11/12): the target is PG's prepared ceiling (281,941),
  and each doc's slice is accepted by its share of that gap closing.

Every doc's §8 "performance verification" therefore assumes the `-M prepared`
harness and, for write-path docs, assumes doc 09 has restored one-commit-per-
transaction.

## 5. Open questions

- **O-BM-1** — The goopg `-S -M prepared` run aborts on
  `mvcc: unknown transaction` (§3) under 50 sustained clients. A clean full-120 s
  read figure needs the doc-09 proc-slot fix (or a re-run that dodges the
  collision); until then the `-S` gap is read off the pre-abort ticks. This is a
  *bug to fix* (via doc 09), not just a measurement nicety.
- **O-BM-2** — *(delivered)* the `QMODE` knob is implemented (default
  `prepared`); `QMODE=simple` reproduces 06/07 for attribution-by-difference.
