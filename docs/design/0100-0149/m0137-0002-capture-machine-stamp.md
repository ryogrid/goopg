# M0137-0002 — machine-stamp every capture artefact

Status: accepted (landed 2026-09-15)

## Context

`AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143" and the M0137
milestone doc's Definition of Done both name the defect this task closes in
R122 §10's own words: a capture's arm attribution "rests entirely on filename
convention". The `<header>` argument `scripts/capture-tpch.sh` /
`scripts/capture-tpcds.sh` write to the top of every artefact (M0137-0001) is
hand-typed prose — nothing checks it against the server that actually
answered, the flags actually in force, or the statistics actually loaded. R120
B1 is the concrete cost: a flag-OFF run was cited as ON evidence because
nothing in the artefact contradicted the filename.

The fix for the SAME class of bug already exists for the TPC-DS SF0.25 sweep
(`docs/design/` 0124-0001 rule D4a / M0127-P5.9-q): `scripts/lib/bench-engine-id.sh`
(engine-id + running-vs-on-disk binary sha, read via `/proc/<pid>/exe`) and
`scripts/planner-flags.sh` (the planner-flag arm, generated from the Go
defaults so a flipped default can't silently mis-stamp — it shipped wrong
twice, identically, before that fix). Both are proven, tested machinery.
Rather than inventing a second stamp format, this task wires the SAME two
libraries into the EXPLAIN-capture pipeline and adds the two fields they do
not already cover: a stats-epoch fingerprint, and — because a capture has no
datadir in scope the way a bench harness does — a datadir-driven binary/PID
verification distinct enough to report path, inode and liveness, not only a
hash.

## Decision

1. **New shared library, `scripts/lib/capture-stamp.sh`**, sourced by both
   capture scripts. Exposes `capture_stamp_block <port> <db> <user> <datadir>
   <pin_desc> <tagbase>`, which emits six lines:
   - `# engine-id: …` — `bench_engine_id()` verbatim (committed-tree hash +
     uncommitted-diff digest of `internal`/`cmd`).
   - `# repo-head: …` — `git log --oneline -1`, with a `[DIRTY]` suffix if
     `git status --porcelain` is non-empty (captures a fact `engine-id`
     already encodes numerically but a human reads faster in this form).
   - `# planner-flags: …` — `planner_flags_body()` verbatim: every flag in
     `scripts/planner-flags.env`'s generated table, exported value or
     generated default label, retired flags always showing their retirement
     marker.
   - `# pinned-GUCs: …` — the caller's session `SET` list as text (currently
     `work_mem=64MB max_parallel_workers_per_gather=4`, matching the `PIN`
     array both scripts already apply).
   - `# engine-binary: …` — see point 2.
   - `# stats-epoch: …` — see point 3.
2. **Binary/PID verification needs a datadir the capture scripts did not
   previously take.** Both scripts gain an **optional 6th positional arg**,
   `[datadir]` (`usage: capture-tpch.sh <port> <db> <user> <outfile> <header>
   [datadir]`, and the `-tpcds` sibling identically). `DATADIR="${6:-}"`.
   With a datadir, `_capture_stamp_binary_line` reads `<datadir>/postmaster.pid`
   the way `bench_running_engine_sha` does, then reports `pid=<pid>
   pid-alive=<yes|no> path=<readlink -f /proc/<pid>/exe> inode=<stat -c %i>
   sha=<sha256sum, 16 hex>` — the Definition of Done names path, inode and PID
   verification as three separate facts, not one hash standing in for all
   three, so all three are printed. Without a datadir (every pre-existing
   caller, including `capture-idempotent-test.py`), the field reads
   `UNKNOWN(no datadir given — pass a 6th arg to stamp the serving binary)`
   rather than guessing at a PID from the port (a listening-socket lookup
   would need elevated permissions this script should not assume, and would
   silently pick up a *different* process on a port a stale server still
   holds — the exact false-positive `pg_isready` READY already produces per
   AGENT.md's "Server traps").
3. **`stats-epoch` is a fingerprint, not a counter.** goopg's
   `pg_stat_user_tables` always reports `last_analyze`/`last_autoanalyze` as
   NULL (`PGStatTablesRowsForDBOid`, `internal/catalog/catalog.go`: "goopg has
   no incremental pgstat mutation counters... every last_* timestamp is
   NULL"), so a timestamp cannot serve as the epoch on both arms of a
   goopg-vs-PG comparison. `n_live_tup` IS comparable on both: PG's live
   counter and goopg's ANALYZE-persisted `reltuples` (same source comment
   names it "the best available signal absent a live counter"). The stamp
   therefore hashes `(relname, n_live_tup)` over every row of
   `pg_stat_user_tables` (`sha256sum`, first 16 hex chars) — this fingerprint
   changes exactly when a values sweep re-samples statistics on either
   engine, which is what "stats epoch" means in
   `METHODOLOGY3/03-process-retrospective.md`'s "Stats-epoch drift" finding
   (1.31x on Q9). The query runs through a **deterministic scratch file**
   named from the caller's `$OUT` basename (`goopg-parity-stats-epoch-<tagbase>.sql`),
   never `$$` — the same K18 discipline M0137-0001 fixed, applied to the new
   query site so this task does not reopen that trap.
   - This task only **stamps** the fingerprint. M0137-0006 ("make the
     stats-epoch declaration a checked step") is the task that turns "declare
     the epoch, re-take the OFF baseline after a sweep" into an enforced gate
     rather than a remembered rule; that is explicitly out of scope here.
4. **Every field degrades to an explicit `UNKNOWN(reason)`, never a guess or
   a silent omission** — the same rule `scripts/planner-flags.sh` states for
   its own missing-env-file case. Verified for: no datadir given, a datadir
   with no `postmaster.pid`, and a live connection failure on the stats-epoch
   query (manual checks below).
5. **Both capture scripts' header-write blocks** now emit the caller's
   `# <header>` line followed immediately by `capture_stamp_block`'s six
   lines, before any `=== Qn` section — the stamp is header material, read
   once per artefact, not per query.

## What was not done (scope boundary)

- **Stats-epoch as a checked/enforced step** (declare-before-sweep,
  re-verify-after) is M0137-0006, not this task.
- **A canonical datadir-lookup-by-port table** was considered (so callers
  would not need to pass `[datadir]` by hand) and rejected: CLAUDE.md's port
  table already exists and duplicating it in shell risks drifting from it the
  same way the retired round-directory scripts drifted from each other. The
  caller supplies the datadir it already knows.
- Real PG's own binary rarely changes mid-campaign, so `UNKNOWN(no datadir
  given)` on the PG arm is an accepted, lower-value gap; passing PG's datadir
  works identically (PG writes the same `postmaster.pid` shape) and is
  available to any caller who wants full symmetry.

## Verification

- `bash -n scripts/capture-tpch.sh scripts/capture-tpcds.sh
  scripts/lib/capture-stamp.sh` — syntax check.
- `python3 scripts/capture-idempotent-test.py -v` — 4/4 pass:
  - the two pre-existing idempotency tests, extended to assert all six stamp
    fields are present and that the no-datadir call degrades to the
    `UNKNOWN(no datadir given` binary line (the before/after this task
    closes: before, the header carried none of these fields at all).
  - two new tests (`_run_with_datadir`, one per script) that pass a
    fixture `postmaster.pid` containing the test process's own PID and
    assert the binary line is populated (`pid=<pid> pid-alive=yes …`), not
    `UNKNOWN` — proving the verification path, not only its absence.
- Manual (not committed, scratch runs): a no-arg capture shows
  `UNKNOWN(no datadir given …)`; a capture with a nonexistent datadir shows
  `UNKNOWN(no postmaster.pid under …)`; a capture with a real PID file shows
  populated `pid=/pid-alive=/path=/inode=/sha=` fields; a capture against an
  unreachable port shows `stats-epoch: UNKNOWN(query failed: …)` instead of
  crashing the script (`set -uo pipefail` without `-e`, and the stats-epoch
  helper never propagates a non-zero `psql` exit past its own function).
- No production (planner/executor) code touched; this is an instrument-only
  change per the M0137 charter.
