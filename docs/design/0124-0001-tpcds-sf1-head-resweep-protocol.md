# 0124-0001 — TPC-DS SF=1 dual-engine re-sweep at HEAD

Status: draft
Date: 2026-07-28
Milestone: M0124-0001 (`docs/design/tpcds-round2-fixes/README.md` §13.5 action 1)

## Problem

§13.3 states goopg's TPC-DS position at HEAD as a **projection**: "A fresh SF=1 dual-engine
sweep at HEAD is the single highest-value next action; until it runs, no claim about SF=1 at
HEAD is better than inference."

The projection combines two incompatible measurement sets:

| set | what it is | why it is not the answer |
|---|---|---|
| A | SF=1, both engines, uniform 600 s — `analysis/tpcds-sf1-goopg-20260727.md` §5.2 | **predates RC-1b** (`5db0a067`) |
| B | two SF0.5 goopg-only sweeps — `sweep-20260727-120937.txt` (PASS=74 MISMATCH=5 ERROR=2 TIMEOUT=14 SKIP=4) and `sweep-20260727-214619.txt` (PASS=74 MISMATCH=3 ERROR=2 TIMEOUT=16 SKIP=4) | ran at **300 s** and **180 s**. For the timeout class they are not comparable — Q88 "entered" it purely because its 228 s exceeded the new 180 s budget. **Also mis-provenanced** — see below |

> **The "post-RC-1b" SF0.5 sweep is labelled with RC-1b's parent commit.**
> `sweep-20260727-214619.txt`'s header reads `# goopg: 27d2dae8` (2026-07-27 21:42), but
> RC-1b `5db0a067` was committed at 23:36 — the sweep started at 21:46 and therefore ran from
> an **uncommitted working tree**. Its `Q50 PASS 6 rows` proves the fix was in the binary, so
> the results are usable, but the provenance line is wrong. This is exactly the failure D1
> below exists to prevent, and any document citing that file must carry this caveat.

That trap already produced a false headline (`TIMEOUT 14 → 16`) and had earlier caused a
300 s SF=1 sweep to be discarded because Q18 (358 s) would have read as a regression. This
protocol makes it structurally impossible to repeat.

## Design

### D1. One sweep, one budget, one commit

- `scripts/tpcds-bench-compare.sh`, `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, all 99 queries.
  Its three 2026-07-27 defects (PATH loss, `tail -1` row extraction, concurrent `&`/`wait`)
  are already fixed and documented in its header.
- Endpoints from `bench/tpcds/env_tpcds.sh`. **The arms differ in database and role**: goopg
  is `-U postgres -d postgres` on **65436**; PostgreSQL 18.3 is `-U ryo -d tpcds` on
  **65438** (`scripts/tpcds-bench-compare.sh:56-57`). Lifecycle via
  `bench/tpcds/server.sh {start|stop} sf1|pg`.
- **One goopg commit, recorded in the report title.** It becomes M0125's delta baseline, so
  it must be an ancestor of every M0125 commit. If any code change lands during the window,
  the sweep is void.

> **Correction to inherit, not repeat.** §1.4's reproduction environment is stale: it names
> goopg `:65433` / PG `:65432` under `bench/tpch/`. Those are TPC-H only since the
> 2026-07-27 bench reorg. Following §1.4 literally would re-create the accident that reorg
> fixed — a TPC-DS load overwriting the TPC-H cluster. §13 is an appended status record that
> did not edit the sections above it, so the correction is recorded here rather than by
> rewriting §1.4.

### D2. Budget-invariance rule

A cell may be compared to a prior sweep **only** at the same per-query budget. Every table
carries its budget in the header. A query completing above a previous sweep's budget is
**budget-incomparable** — never "regressed" and never "entered the timeout class". This rule
binds this document too: no SF0.5 number may be quoted as an SF=1 prediction.

### D3. Server-age hygiene and the GC regime

A goopg server that has just run a 600 s timeout sits at `GOMEMLIMIT` with `GOGC=off` and
thrashes GC. `RESTART_AFTER_TIMEOUT=1` (the default) restarts the SF=1 server after each
goopg TIMEOUT; keep it.

State the regime, because it is **not** the TPC-H one: `bench/tpcds/env_tpcds.sh` exports
`GOGC=off` and `GOMEMLIMIT=12GiB`, so every TPC-DS number here is taken with the collector
off, while `0124-0002` prescribes `GOGC=100` for the TPC-H arms. The restart mitigates
*carry-over*, not the regime. Never set a cgroup `memory.high` below `GOMEMLIMIT`: `GOGC=off`
plus a low `memory.high` produces a permanent kernel-throttle band after one big query,
which mimics a code regression.

### D4. Orphan reaping — a script change, not just a step

`timeout N psql` kills only the **client**; the PostgreSQL backend keeps executing and
contaminates every later timing. **`reap_pg_orphans` exists only in
`scripts/tpcds-sf05-regression.sh`; `scripts/tpcds-bench-compare.sh` has no equivalent.**
Port it before the sweep. It must materialise the victim set first — SQL does not guarantee
evaluation order, and a bare `WHERE … AND pg_terminate_backend(pid)` has already killed a
healthy backend in this programme (the Q6 incident):

```sql
WITH victims AS MATERIALIZED (
  SELECT pid FROM pg_stat_activity
   WHERE datname = 'tpcds' AND pid <> pg_backend_pid()
     AND backend_type = 'client backend'
     AND state = 'active'
     AND now() - query_start > interval '600 seconds'
)
SELECT count(*) FROM victims WHERE pg_terminate_backend(pid);
```

Match the SF0.5 predicate exactly — `backend_type = 'client backend'` **and** `state = 'active'`.
A looser `state <> 'idle'` also matches `idle in transaction`, which is a silent widening of a
statement that kills backends.

This qualifies §13.1 phase 5's "harness fixes — landed": the SF=1 harness still lacks a
hazard the SF0.5 harness codifies.

> **LANDED 2026-07-28** (`scripts/tpcds-bench-compare.sh`, `reap_pg_orphans`). The port keeps
> both load-bearing properties (MATERIALIZED victim set, `client backend` + `active`), takes its
> interval from `TIMEOUT_SEC` and its endpoint from `PG_PSQL`, and is called only on a `pg`
> arm whose `LAST_STATUS` is `TIMEOUT`, printing the number terminated. Verified against the
> live 65438 cluster with no orphans present: returns `0`, exit 0 — i.e. the no-victim case is
> a no-op, not an error.

### D4a. Engine provenance is a binary property, not a `git log` line

Chunked execution (fix_plan's "Chunked execution" note) splits the sweep across invocations,
and the stated invariant was "all chunk headers must name the same SHA". Measured while
porting D4, `git log --oneline -1` is wrong in **both** directions:

- it **changes** when a docs/tracker commit lands between chunks — not a code change, but it
  reads as one; and
- it does **not** change when the engine differs. `bench/tpcds/server.sh start` runs
  `go build -o tmp/goopg-bench-bin ./cmd/goopg` **from the working tree**, and
  `RESTART_AFTER_TIMEOUT=1` calls it after every goopg TIMEOUT. An uncommitted engine edit
  therefore enters the sweep at the next bounce, under an unchanged header. This is the
  mechanism behind the mis-provenanced sweep in "Problem" above.

Worse, the on-disk binary is not necessarily the one *serving* the cluster. Live state at the
start of this loop: the SF=1 server had been up 16 h on image `4140b160…` (shown by
`/proc/<pid>/exe` as `(deleted)`) while `tmp/goopg-bench-bin` was `7a4b4f7b…`. A header naming
either the commit or the on-disk build would have described an engine that answered nothing.

The header therefore carries three lines: `git log -1` (kept, for readability),
`# engine-id:`, and `# engine-binary: running=<sha> on-disk=<sha>`, where `running` is the
sha256 of `/proc/$(head -1 $TPCDS_PGDATA/postmaster.pid)/exe`. An on-disk/running mismatch
prints a restart warning before any query runs, and `restart_goopg` re-checks `engine-id`
after each rebuild, printing `*** SWEEP VOID: engine source changed mid-sweep ***`.

**`engine-id`, not the binary's sha256 — corrected on chunk 1 of this sweep.** The first cut
compared the binary image, on the reasoning that `go build` is deterministic (it is: two
builds of an unchanged tree gave one sha). That is true and irrelevant, because the toolchain
stamps `vcs.revision`, `vcs.time` and `vcs.modified` into the image. The docs-only commit
that landed these very harness changes moved the image `e6774c4f → 8f0aac15`, and the guard
duly cried `SWEEP VOID` over an engine whose source had not changed — a **false positive that
would have condemned a good 21-minute chunk**. Proof the source was identical:
`git diff --stat 6d6bd1ea^ 6d6bd1ea -- internal cmd` empty, `git status --porcelain --
internal cmd` empty, and `go build -buildvcs=false` reproducing one image (`33e7d081…`)
across both revisions.

`engine-id` is therefore the committed engine trees **plus** a digest of any uncommitted
engine edit:

```sh
git rev-parse HEAD:internal HEAD:cmd            # committed engine state
git diff HEAD -- internal cmd | sha256sum       # uncommitted engine state
```

Neither term moves for a docs or tracker commit; the second term is exactly what the
mis-provenanced `sweep-20260727-214619.txt` needed and lacked. The binary sha stays in the
header as provenance for *which image answered*, which the commit line cannot express.

**Comparability rule, restated:** chunks are comparable when `engine-id` matches. A differing
`git log -1` **or** a differing binary sha with identical `engine-id` is a docs/tracker commit
and voids nothing — the sweep's own `chunk-1-4.txt` is the worked example, annotated in
`analysis/tpcds-sf1-resweep-20260728/RESULTS.md`.

### D5. State labelling (§8)

Every goopg number is **S-cold**. Record the proof before the sweep and paste it into the
report:

```
psql -h 127.0.0.1 -p 65436 -U postgres -d postgres -c \
  "select relname, reltuples::bigint, relpages from pg_class
    where relname in ('store_sales','catalog_sales','web_sales','inventory',
                      'customer','date_dim','item','store') order by 1;"
psql -h 127.0.0.1 -p 65436 -U postgres -d postgres -c \
  "select count(*) from pg_stats where schemaname='public';"
```

Zero `reltuples` ⇒ S-cold, the state M0125-0003 is designed to change. Non-zero means this
is not the baseline M0125 needs.

> **Correction 2026-07-28 — the original predicate made the proof vacuous.** This section first
> filtered `where relnamespace='public'::regnamespace`. On goopg that returns **`(0 rows)` with
> no error**: `regnamespace` is not implemented as PG's name-resolving type (`'public'::regnamespace::oid`
> errors with `invalid input syntax for type oid: "public"`), so the predicate matches nothing.
> "Zero rows" then satisfies "zero `reltuples`" *by construction* — it would have read as S-cold
> on a fully ANALYZEd cluster too. The named-relation form above is the actual proof. Ledger row
> 2026-07-28 records the missing `regnamespace` input function.
>
> Proof captured for this sweep — `analysis/tpcds-sf1-resweep-20260728/s-cold-proof.txt`:
> all four probed relations report `reltuples=0 relpages=0` (`relnamespace=2200`), and
> `count(*) from pg_stats where schemaname='public'` = **0**, with the data confirmed loaded
> (`select count(*) from store_sales` = 2 880 404 = SF=1). GC regime `GOGC=off`,
> `GOMEMLIMIT=12GiB`.

### D6. Classification and row counting

Status is classified on psql's `ERROR:` / `FATAL:` / `PANIC:` **line prefix**, not a
case-insensitive substring anywhere in the output. Row counts sum **every** `(N rows)` block
— Q14, Q23, Q24 and Q39 hold two statements each. Q36/Q70/Q86 stay SKIP (`PG_SKIP`); Q4 is
reported but excluded from "goopg-only" (PG times out too).

**Budget-marginal sub-class (added 2026-07-28, loop #3 — measured, not hypothetical).** A
TIMEOUT verdict carries information only when the query's true runtime is *unbounded above* by
the cut. Q18 proved the other case exists: set A recorded `OK 626 s / 100` and this sweep
`TIMEOUT 627 s / 0` — one second apart, i.e. the same work landing on opposite sides of the
600 s cut. So the timeout class splits in two, and reports must keep them apart:

- **unbounded** — no run at this budget has seen the query finish (Q5, Q10, Q14). A verdict
  change here is signal.
- **budget-marginal** — some run has finished it within ~5 % of the budget (Q18; Q82 at
  ~576 s is the next candidate, already flagged "watch for flapping" in D7). A verdict change
  here is a re-rolled coin and **must not be reported as a fix or a regression.** To make such
  a cell informative, classify it by measured runtime rather than by verdict, or re-measure it
  at a larger budget — never by re-running at the same budget until it reads the desired way.

Note that a cell's elapsed figure covers query **plus** the ≤30 s EXPLAIN capture, which sits
outside the timeout-guarded query; that is why an `OK` cell can report an elapsed above the
budget at all, and why elapsed is not directly comparable to `TIMEOUT_SEC`.

Where M0124-0005 has landed, capture the per-query result checksum in the same pass — this
harness already writes `*_result.txt` per query and engine, so it is nearly free. (Note the
SF0.5 sweep does **not** write result files; see `0124-0005`.)

### D7. Deliverable

`analysis/tpcds-sf1-goopg-<YYYYMMDD>.md`: provenance (commit, budget, cluster paths, S-cold
proof, GC regime); the defect table; then a **confirm/refute line for each projection**,
since the point of the sweep is to test §13.3, not restate it. Every expectation below is an
**SF=1** number:

| query | expected at HEAD | source |
|---|---|---|
| Q50 | PASS, 6 rows | fixed by RC-1b (SF0.5-confirmed; SF=1 unmeasured) |
| Q39 | PASS, 236 rows = PG | `927472e0`, set A |
| Q75 | **ERROR, division by zero** | new, caused by RC-1b |
| Q72 | **TIMEOUT** (was MISMATCH 0/100 in 7 s at SF0.5) | wrong → slow |
| Q8 | ERROR `XX000`, server survives | contained, not fixed |
| Q47, Q49, Q51 | MISMATCH (0/100, 30/34, 0/100) | unchanged |
| Q35 | TIMEOUT 651 s in set A; completed at **525 s** in the 07-26 sweep | see M0124-0004 |
| Q82 | OK ~576 s / 2 rows — within 4 % of the budget; watch for flapping | set A |
| Q88 | **TIMEOUT 660 s / 0** | set A. The 228 s / 1 row figure is SF0.5 — do not import it |
| Q34, Q46 | OK, 374 and 100 rows = PG | set A |

**These rows are the projections under test, not predictions this document endorses**, and
several (Q75, Q72) are extrapolated from SF0.5 — which D2 forbids as a *conclusion*. Testing an
SF0.5-derived hypothesis at SF=1 is the point of the sweep; quoting one as an SF=1 result is
what D2 bans. Note in particular that set A measured Q72 at SF=1 as **OK 14 s / 0 rows**, so
"TIMEOUT" is a hypothesis with a measured contradiction at this scale.

Close with the resulting goopg-only defect count, replacing §13.3's projected 21.

## Non-goals

No fix, no flag change, no planner commit in the window. No SF0.5 comparison in this report.
No EXPLAIN-cost analysis — cost and width are hardcoded literals
(`internal/executor/operators_explain.go:378`, `:925`); only plan **shape** and
`EXPLAIN ANALYZE` **actual** rows are signal.

## Cost and risk

Budget from set A's own wall clock, not an estimate: **16** goopg TIMEOUTs averaging ~652 s
(≈2.9 h) plus a restart each, ~3 PG timeouts (Q4/Q11/Q74), Q82 ~576 s, plus the completing
queries — set A's measured total is ~5.3 h of pure query time (goopg 4.6 h + PG 0.7 h). **Plan for
8–10 h, not 4–5.** A mid-sweep restart silently returns a batch to a different state — check
`bench/tpcds/runtime_goopg/goopg.tpcds.log` for restart boundaries before trusting any batch.

## Gate

Docs plus the `reap_pg_orphans` port (a shell change → units + the pre-commit hook). No
engine change, so no TPC-H run.
