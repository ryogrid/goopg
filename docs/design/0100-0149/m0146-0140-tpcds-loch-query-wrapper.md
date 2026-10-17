# M0146-0140 — TPC-DS Q36/Q70/Q86: the setup's subquery wrapper broke the queries

Status: done 2026-10-09 (1419b3106). Parent: M0146-0014a.

## Problem

The M0146-0014a census recorded TPC-DS Q36, Q70 and Q86 as capture errors
at both scales: both engines reported `syntax error at or near ";"` at
`limit 100;`. The sweep's oracle had carried them as `SKIP_QUERYGEN`
("dsqgen artefacts that fail on PG too") since the TPC-DS harness was
built, so no gate had ever compared them.

## Cause

The fault was not in the capture wrapper the task named
(`scripts/capture-tpcds.sh` splits a file on `;` and EXPLAIN-prefixes each
statement, which is correct). It was in the generated query files.

- These three queries order by the output alias `lochierarchy` inside an
  expression (`case when lochierarchy = 0 then ... end`). PG resolves a
  bare output alias in ORDER BY, but not one nested in an expression.
- Upstream `third-party/tpcds-postgres/split_sqls.py` handles this by
  wrapping the SELECT in `select * from (...) as sub` and moving the
  final ORDER BY, with its LIMIT, outside the wrapper.
- `scripts/tpcds-setup.sh` wrapped the **whole file** instead, so
  `order by ... limit 100;` ended up inside the parentheses. The capture's
  `;` split then produced two broken statements, and every engine
  rejected them.
- `scripts/tpcds-bench.sh`'s `fix_pg_query` had the same wrap and ran it
  in place before every query on every run, so it would nest one more
  `select * from (` per run.

## Change

- **`scripts/tpcds_fix_loch_queries.py`** (new) applies the upstream form.
  It also repairs a file already in the broken form, and leaves a fixed
  file unchanged, so it is idempotent.
- **Generators.** `tpcds-setup.sh` and `tpcds-bench.sh` call the helper.
  `tpcds-bench-compare.sh`'s PG skip list is now empty.
- **SF0.25 sweep** (`tpcds-sf025-regression.sh`):
  - `ensure_query_fixes` runs the helper at `sweep`/`oracle` start, so a
    tree generated before this change is repaired in place. The query
    files are gitignored data, so the repair cannot ride a commit.
  - `PG_SKIP` is now empty. A new `ENGINE_GAP` list (Q70) captures PG's
    rows and checksum under status `SKIP_ENGINE_GAP`, which the sweep
    skips. The engine fix flips that row to `OK` without a new PG run.
- **Oracle.** Rows 36/70/86 were re-captured from :65438 (plain SELECTs,
  `QUERIES=36,70,86` into a scratch file, spliced in). The other 96 rows
  are unchanged.

## What the three queries showed

- **Q36, Q86.** goopg's results are byte-identical to PG's (100 rows
  each; `ck=n/a` because the LIMIT saturates). Both now produce plans on
  both engines, so the next parity census records them.
- **Q70.** PG returns 3 rows. goopg fails with `aggregate call could not
  be resolved`. Filed as **M0146-0143**:
  - `collectAggregateCalls` (planner.go) collects aggregates through
    `walkExpr`, which descends only operators, casts and function
    arguments.
  - So an aggregate that appears only inside a window's PARTITION BY or
    ORDER BY (`rank() over (order by sum(b)) ... group by a`), or only
    inside a CASE (`case when sum(b) > 2 then 1 end`), is never
    collected.
  - The same query works once that aggregate also appears in the select
    list.

## Verification

- **SF0.25 sweep:** PASS=98 (60 ck-verified, 38 ck=n/a); MISMATCH,
  CKMISMATCH, ERROR and TIMEOUT all 0; SKIP=1 (Q70). Before: PASS=96,
  SKIP=3. The plan channel lists Q36/Q70/Q86 as changed only because they
  were errors before.
- **Helper:** idempotent on a second run; it repairs the broken form and
  gives the same text as a fresh conversion.

## Not covered (ledgered)

- **Q70's engine gap** (M0146-0143).
- **`bench/tpcds/plans-pg/` Q36/Q70/Q86** were left as `SKIP_QUERYGEN`
  stubs, on the reasoning that recapturing them would move the
  estimate-parity ratchet. That was backwards: as stubs they made the
  ratchet report Q36/Q86 as NEW findings, because it had no PG reference
  to score them against. M0146-0141 recaptured the three files
  (a4f6b8c48), and the ratchet is back to 1 finding.
