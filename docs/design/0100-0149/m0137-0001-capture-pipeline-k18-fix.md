# M0137-0001 — canonical parity capture pipeline + K18 `$$`-trap fix

Status: accepted (landed 2026-09-15)

## Context

`AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143" names M0137 as
the first task in the plan-parity milestone group and a prerequisite for the
other six: every one of them is judged by instruments this milestone repairs.
M0137-0001 is the first of those repairs.

Two divergent copies of the TPC-H/TPC-DS EXPLAIN-plan capture scripts existed
inside round directories:
`docs/design/not_ralph/plan_parity_fix_take2/methodology/{capture-tpch,capture-tpcds}.sh`
and `.../r2-instrument/{capture-tpch,capture-tpcds}.sh`. Both embed the
capturing shell process's PID (`$$`) into the scratch SQL file's name
(`parity-capture-$$.sql`). Both wrote that in a comment as if it were the
*fix* for a `mktemp`-based version: "Fixed name, not mktemp: the temp path
appears in psql ERROR text for the unplannable queries, so a random name
fakes a plan diff." That comment is wrong. `$$` is the PID of the capturing
process — it is not fixed across invocations, only within one. For any query
that fails to plan (TPC-H/TPC-DS both have some — Q36/Q70/Q86 fail to parse
on both engines), `psql -f <path>` prefixes its error output with the path it
was given: `psql:<path>:<line>: ERROR: ...`. So the PID leaked into every
capture that hit an unplannable query, and two captures of the exact same
server produced a spurious diff purely from that leak. `AGENT.md` records this
as live in R122, R123, R124 and R128 — four separate rounds burned on a false
structural reading.

## Decision

1. **One canonical copy, in `scripts/`.** `scripts/capture-tpch.sh` and
   `scripts/capture-tpcds.sh`, both `<port> <db> <user> <outfile> <header>`
   (the `r2-instrument` signature; the `methodology` TPC-DS variant took only
   3 args and hardcoded `db=user=postgres` — dropped for consistency with the
   TPC-H script and with every other port/db/user-taking script in
   `scripts/`).
2. **Fix the trap at the root: derive the scratch filename from `$OUT`, not
   `$$`.** `$OUT` is caller-chosen and constant across repeated invocations
   of the same capture command (the actual real-world case: re-running a
   capture to confirm it is stable, or re-capturing after a no-op change).
   `tmp="${TMPDIR:-/tmp}/goopg-parity-capture-$(basename "$OUT").sql"`.
   Concurrent captures into different `$OUT` files (e.g. two flag arms
   running in parallel) still get distinct scratch paths, so this does not
   reintroduce a collision the PID-based name accidentally avoided.
3. **`scripts/capture-idempotent-test.py`** — a regression test that stubs
   `psql` (via `$PG_BIN`, which both scripts already prepend onto `PATH`
   ahead of everything else) so it needs no live cluster. The stub answers
   one query with a fake plan and one with a `psql`-style
   filename-prefixed `ERROR` line, reproducing the exact leak surface. The
   test runs each capture script twice against the same `$OUT` and asserts
   byte-identical output. Verified (manually, not committed) that
   reintroducing `$$` into a scratch copy of `capture-tpch.sh` makes the
   test fail with a real diff — the test is not vacuous.
4. **TPC-DS sections normalised to `=== Qn`** (matching
   `scripts/pg-plan-parity-diff.py`'s `SECTION_RE`, which the old
   `===== Qn =====` form from the `methodology` variant does not match)
   directly at capture time, so the `sed`-based post-capture normalisation
   step documented in `METHODOLOGY.md` §4.3 is no longer needed for
   artefacts captured with the new script.
5. **PATH/binary resolution follows the existing `scripts/pg-oracle-diff.sh`
   convention** (`REPO_ROOT`-relative `PG_BIN`, overridable via env) instead
   of the retired copies' hardcoded `/home/ryo/work/goopg/goopg/...` path —
   both so the scripts work from any checkout and so the test can inject a
   stub `psql` without touching `PATH` ordering.
6. **The two round-directory copies are retired**: `METHODOLOGY.md` §4.1 (the
   still-live measurement-pipeline doc per `METHODOLOGY3/README.md`) is
   updated to point at `scripts/`. The round directories themselves
   (`r2-instrument/`, `methodology/`) are left untouched as historical
   artefacts — `AGENT.md`'s "What to read, and what not to read" states they
   are raw evidence, not guidance, and editing frozen round reports would
   falsify history.

## What was not done (scope boundary)

`scripts/capture-tpch.sh`'s TPC-H query source
(`${TPCH_QUERY_DIR:-/tmp/parity-r0/queries/tpch}`) is not git-tracked and
lives under `/tmp`. This is a pre-existing property of both retired copies,
not something this task introduced, and the fix_plan line for M0137-0001
scopes this task to the `$$` trap and de-duplication only. Filed as a
deferral ledger row (2026-09-15, M0137-0001): the durable source is
`internal/testutil/tpch` (`tpch.Queries()` / `tpch.Q15ViewBody()`), which is
also what `cmd/estimate-audit` reads; M0137-0003 ("write the canonical
baseline-capture procedure") is the task that should settle whether
`capture-tpch.sh` migrates to read from there, gets a dump step, or is
subordinated entirely to `estimate-audit -plan-only` (which `AGENT.md`
already states is the correct source for TPC-H *baselines* — this script's
continuing role is the lighter-weight EXPLAIN-only capture used by
arm-comparison work, not baseline capture).

## Verification

- `bash -n scripts/capture-tpch.sh scripts/capture-tpcds.sh` — syntax check.
- `python3 scripts/capture-idempotent-test.py -v` — 2/2 pass (stubbed, no
  live cluster).
- Manual: patched a scratch copy of `capture-tpch.sh` to reintroduce
  `parity-capture-$$.sql` and re-ran the idempotency check against it —
  fails with a real PID-driven diff, confirming the test has power.
- No production (planner/executor) code touched; this is an instrument-only
  change per the M0137 charter.
