# M0137-0003 — the canonical baseline-capture procedure

Status: accepted (landed 2026-09-15)

## Context

`AGENT.md` §"Plan-parity harness" and the M0137 milestone doc's scope line
both name this task's job in one sentence: "write down the canonical
baseline-capture procedure — including the fact that **TPC-H baselines come
from `estimate-audit -plan-only`, not `capture-tpch.sh`**... and that
`-serial` defaults **true**". Two things made this task necessary rather than
a formality:

1. **AGENT.md's own procedural home for this information no longer exists.**
   Commit `baf40efcb` deleted a 167-line "plan-parity-take2 work appendix"
   (bootstrap PATH exports, the port/db/user/password table, server
   lifecycle, "where things live") in the same commit that filed M0137-0003
   to replace it — but the pointer text at AGENT.md's "Bootstrap first" and
   "Canonical captures" bullets still says "read its §0 and §1" and "use the
   appendix's commands directly". Both now point at nothing. This task is the
   promised replacement.
2. **The TPC-H mistake has been made twice on record, three weeks apart.**
   `docs/design/not_ralph/plan_parity_fix_take2/TODO.md:4861-4875` (path named
   directly in this task's own milestone-doc line, so citable per the
   plan-parity harness's round-directory access rule) records "METHODOLOGY
   TRAP RE-HIT AND RECORDED 2026-09-14 (cost ~1 capture pair, same trap as R65
   §0)": `r2-instrument/capture-tpch.sh` opens a fresh `psql` session per
   query and never `ANALYZE`s; goopg's ANALYZE statistics are per-connection
   (`cmd/estimate-audit/main.go:37-38,:286,:325`); so every goopg TPC-H plan
   it captured that round was planned on **empty** stats — Q21 alone scored
   `cost=16.54` against PG's `268796`, a default-estimate tell. **The same
   entry also settles the TPC-DS side of this task**: "TPC-DS is NOT
   affected — probed directly 2026-09-14 (query7 cold-session vs
   ANALYZE-warmed-session plans identical in shape, costs within 0.03%), so
   its cluster's stats are effectively persistent and `capture-tpcds.sh`
   remains valid there." That is the one piece of live verification this
   procedure rests on for the TPC-DS half; this task does not re-derive it,
   only writes it down where a script's own header and a Ralph loop's search
   order (METHODOLOGY3 → milestone doc → task line, never a bare round-dir
   grep) will actually find it before the mistake gets made a third time.

Also deferred here and now resolved: the M0137-0001 ledger row on
`TPCH_QUERY_DIR`'s untracked `/tmp` default (`.ralph/deferral_ledger.md`,
task-id M0137-0001) named this task as its resume point. §5 below records the
decision and the small guard-rail fix that closes the harm without a larger
scope increase.

## Decision — the procedure

### 0. Bootstrap (every fresh shell)

```bash
cd /home/ryo/work/goopg/goopg   # $REPO_ROOT; every path below is relative to it
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
which psql pg_isready   # must both resolve
```

`psql`/`pg_isready`/`pg_ctl`/`pgbench` live **only** under
`postgres/local_install/bin/` (the read-only PG 18.3 oracle submodule), never
on a default `PATH`. Without this, `make plan-gate`'s `pg_isready` probe fails
as *command not found* and the gate misreports the server as unreachable —
the same trap AGENT.md's Measurement section already names.

**Set `PGPASSWORD` before touching either cluster.** `docs/design/.../TODO.md:4872-4875`
records the concrete cost of skipping this: without it, `psql -U <user>` (as
both capture scripts and a manual session invoke it) blocks on a **hidden**
password prompt, every query silently eats a 180 s timeout, and the capture
just logs `(capture failed)` with no further explanation.

### 1. Cluster map + auth

| port | engine | db | user | password | data dir |
|---|---|---|---|---|---|
| 65432 | PG 18.3 TPC-H reference | `tpch` | `postgres` | `postgres` | `bench/tpch/runtime/pgdata` |
| 65433 | goopg TPC-H bench (SF=1, HammerDB load) | `tpch` | `tpch` | `tpch` | `bench/tpch/runtime_goopg/data` |
| 65436 | goopg TPC-DS SF=1 | (loopback) | `postgres` | trust | `bench/tpcds/runtime_goopg/data` |
| 65437 | goopg TPC-DS SF=0.25 gate | (loopback) | `postgres` | trust | `bench/tpcds/runtime_goopg/data-sf025` |
| 65438 | PG 18.3 TPC-DS reference | `tpcds` / `tpcds025` | `ryo` | peer/trust | `bench/tpcds/runtime/pgdata` |

Full detail and lifecycle scripts: `CLAUDE.md`'s benchmark-clusters table,
`bench/tpch/README.md`, `bench/tpcds/README.md`. 65434/65435 are reserved
nightly `ci/batch` clone lanes — never use them for interactive work.
`:6543x` is a **shared** block: verify a reference is up, never restart it;
throwaway/manual work uses a private clone on a `55xx` port instead (see
AGENT.md's "Server traps").

### 2. TPC-H baseline: `estimate-audit -plan-only`, not `capture-tpch.sh`

```bash
go build -o /tmp/estimate-audit ./cmd/estimate-audit
PGPASSWORD=tpch /tmp/estimate-audit -plan-only \
  -label <descriptive-label> -out analysis/m0137 \
  -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres
```

- **`-serial` defaults `true`** (`cmd/estimate-audit/main.go:285`) and sets
  `max_parallel_workers_per_gather = 0` on **both** engines
  (`session.ensure`, same file). This is why TPC-H `parallelism` reads 0 in
  every quoted category table — the category is measured **out** of the
  corpus, not solved. Do not report a TPC-H parallelism win or regression
  from a `-plan-only` run; the last real (non-serial) reading was `18→16`
  under the gather-paths flip (`METHODOLOGY2.md` §5).
- **`-plan-only` still warms stats.** `session.ensure`'s `ANALYZE <table>`
  loop over `tpch.Tables()` runs regardless of `-plan-only` (only
  `EXPLAIN ANALYZE` vs plain `EXPLAIN` and the §5/§4 report sections are
  gated on it) — "the stats decide which plan is CHOSEN, so a plan-only run
  without them would measure the spine of the no-stats planner"
  (`main.go`'s own `prefix()` comment). This is the property `capture-tpch.sh`
  does not have and the reason this tool is canonical for TPC-H: one
  connection, opened once, `ANALYZE`s every table before the first `EXPLAIN`,
  so the result never depends on whether some other session already warmed
  the cluster.
- **GUCs are NOT pinned by the tool itself** beyond the one `-serial` `SET`
  above — no `work_mem`, no `effective_cache_size`. Both must already match
  between the two clusters' `postgresql.conf`; verify against
  `bench/tpch/README.md`'s "Cross-engine fairness" table
  (`shared_buffers=2048MB`, `autovacuum=on`, `work_mem=64MB`,
  `effective_cache_size=2GB` on both sides as of 2026-09-06) before trusting
  a capture — a drifted cluster measures configuration, not planning (K10).
- Output: `<out>/<label>.txt` (the §4/§5 report — empty of those sections in
  `-plan-only` mode), `<out>/<label>.plans.txt` (goopg `=== Qn` sections),
  `<out>/<label>.pg.plans.txt` (the PG reference, captured by the **same**
  function with the same `-serial`/warm-stats treatment — "the reference has
  to be measured the same way… or the comparison is between two protocols
  rather than two planners", `main.go`'s `capture` doc comment). Root-cause
  work (the 09 §5 tripwire this tool was originally built for) still defaults
  to `analysis/leftdeep-joins/`; **M0137-series baseline captures use
  `-out analysis/m0137/`** instead, per AGENT.md's "Way of working" rule that
  new raw artefacts for this milestone group do not go into `rNNN-*`
  directories or the tool's legacy default.
- `capture-tpch.sh` is **not** the TPC-H baseline tool. It is demoted to an
  ad-hoc raw-EXPLAIN convenience capture for an **already stats-warm,
  long-running** cluster (e.g. eyeballing a structural diff on the live
  `:65433` bench cluster mid-session, where autovacuum has already analyzed
  everything) — it does not `ANALYZE` anything itself, so pointing it at a
  cold cluster silently reproduces the exact defect this section exists to
  head off.

### 3. TPC-DS baseline: `scripts/capture-tpcds.sh`

```bash
scripts/capture-tpcds.sh 65437 postgres postgres \
  analysis/m0137/<label>-tpcds-goopg.txt "<label> goopg SF0.25" \
  bench/tpcds/runtime_goopg/data-sf025
scripts/capture-tpcds.sh 65438 tpcds025 ryo \
  analysis/m0137/<label>-tpcds-pg.txt "<label> PG18.3 SF0.25 reference"
```

- Unlike TPC-H, a fresh `psql` session per query is **safe** here — see the
  Context section's citation: TPC-DS's cluster stats were probed directly
  (query7, cold session vs an ANALYZE-warmed session) and found
  shape-identical with costs within 0.03%, because the SF=1/SF0.25 load
  procedure ANALYZEs every table once at load time
  (`bench/tpcds/README.md` "3. ANALYZE each table") and that result is
  durable across new connections on this cluster. Do not generalise this to
  TPC-H — the two corpora are asymmetric here for a stated, verified reason,
  not a default assumption.
- GUCs **are** pinned in-session by the script itself
  (`work_mem='64MB' max_parallel_workers_per_gather=4`, identical to
  `capture-tpch.sh`'s `PIN` array) — no cluster-config alignment step is
  needed for this half.
- **Which reference is canonical is still an open question — M0137-0004,
  not this task.** `METHODOLOGY3/README.md`'s executive summary states it
  plainly: TPC-DS `match=2` comes from `capture-tpcds.sh` on `:65437` against
  **live** PG `:65438`; `match=1` comes from the same family against the
  committed `bench/tpcds/plans-pg` fixture. Both are reproducible; the
  programme currently quotes both. This procedure names live `:65438` as the
  worked example above because it is what the higher, more-recent number
  (`match=2`) used, but **do not cite this doc as having settled the
  reference question** — cite M0137-0004's own resolution once it lands, and
  until then keep the non-regression floor at TPC-DS `match >= 1` per
  AGENT.md's "Success criterion".

### 4. Compare

```bash
python3 scripts/pg-plan-parity-diff.py <goopg>.plans.txt <pg>.plans.txt [--verbose]
```

Verdicts: `MATCH / SHAPE-DIFF / UNPARSED / MISSING-NODE / ERROR / TIMEOUT`.
`UNPARSED` must be 0 for any parity claim (AGENT.md's "What the parity
verdict is blind to (K50)" and `METHODOLOGY.md` §4.3 cover the rest of this
tool's behaviour and are not repeated here).

### 5. `capture-tpch.sh`'s `TPCH_QUERY_DIR` — decision and guard-rail

The M0137-0001 deferral (`.ralph/deferral_ledger.md`) posed two options:
(a) add a durable git-tracked query-dump step, or (b) treat
`estimate-audit`'s Go-embedded `tpch.Queries()` as the more-authoritative
path and let the script's `/tmp` dependency stand. §2 above already answers
this: **(b)** — `capture-tpch.sh` is no longer on the TPC-H baseline critical
path, so its query corpus's volatility is no longer safety-critical. What was
still worth fixing directly: the deferral's stated harm was that a missing
`/tmp` seed "silently breaks the canonical script with no error surfaced
beyond per-query 'MISSING QUERY FILE' lines" — a capture that reads as 22
genuine per-query planning failures rather than one clear setup error.
`scripts/capture-tpch.sh` now checks both `TPCH_QUERY_DIR` and
`TPCH_Q15A_FILE` exist before doing any work and exits 1 with a pointer to
this document instead. Relocating the corpus into git tracking (option (a))
remains optional future polish.

## What was not done (scope boundary)

- **The TPC-DS `match=2` vs `match=1` reference question** is named, not
  resolved — M0137-0004.
- **The stats-epoch declaration is not yet a checked/enforced step** — that
  is M0137-0006; this doc only tells a reader which tool to run, not how to
  verify after the fact that nobody re-sampled statistics mid-campaign.
- **`TPCH_QUERY_DIR`'s corpus is not relocated into git tracking** — accepted
  as optional polish now that it is off the baseline critical path (§5).
- **No production planner/executor/catalog code was touched.** This is a
  documentation-and-instrument task per the M0137 charter; the two script
  edits (both `scripts/capture-tpch.sh`, a fail-fast guard) are the harness
  itself, the explicit subject of this milestone.

## Verification

- `bash -n scripts/capture-tpch.sh` — syntax check, clean.
- `python3 scripts/capture-idempotent-test.py -v` — 2/2 test methods pass
  (each exercises the idempotency assertion and the `_run_with_datadir`
  stamp assertion; both already pass `TPCH_QUERY_DIR`/`TPCH_Q15A_FILE`
  through their fixture env, so the new guard is exercised on its
  success path by every existing run).
- **Live, scratch (not committed) end-to-end run of the exact §2 command
  shape**: a throwaway private goopg server (`/tmp/pp2-audit-probe`, port
  5539, started/stopped through `scripts/goopg-test-run.sh`, never a shared
  `:6543x` cluster) loaded with `tpch.DDL()` + `tpch.SampleInserts()`, then
  `estimate-audit -plan-only -queries 1,6` against it. Confirmed: the binary
  builds, the flags parse, the warm-stats `ANALYZE` loop runs without error
  against a freshly created schema, `<label>.plans.txt` sections read
  `=== Q1` / `=== Q6` (matching `pg-plan-parity-diff.py`'s `SECTION_RE` and
  `capture-tpch.sh`/`capture-tpcds.sh`'s own section format), and the
  `-plan-only` banner replaces the §5/§4 sections as documented. Server
  stopped and all scratch files removed after the check; nothing from this
  run is committed, per the same precedent M0137-0002 set for its own manual
  failure-mode checks.
- Guard-rail regression check: manually unset `TPCH_QUERY_DIR`/pointed it at
  a nonexistent directory and confirmed `capture-tpch.sh` exits 1 with the
  new message instead of writing 22 `MISSING QUERY FILE` sections; restored
  the fixture env and reran `capture-idempotent-test.py` green.
- No live run against the real TPC-H (`:65433`) or TPC-DS (`:65436`/`:65437`)
  bench clusters was performed for this task — both were down at task start
  (`pg_isready` "no response") and standing them up (a 12-minute HammerDB
  load, or the TPC-DS SF=1/SF0.25 load procedure) is out of proportion to a
  documentation task whose commands were already validated end-to-end on a
  throwaway server with the identical code path.
