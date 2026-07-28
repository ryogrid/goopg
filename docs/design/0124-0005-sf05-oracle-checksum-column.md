# 0124-0005 — A value checksum for the SF0.5 regression oracle

Status: draft
Date: 2026-07-28
Milestone: M0124-0005 (`docs/design/tpcds-round2-fixes/README.md` §13.4 item 3)

## Problem

The SF0.5 fast regression gate compares **row counts** against a git-tracked PG 18.3 oracle.
It is therefore structurally blind to one defect class: **right row count, wrong values.**

Q75 is the worked example, and it is not hypothetical. Before RC-1b, goopg returned 100 rows
for Q75 — matching PG exactly — while its `all_sales` CTE computed **1,057,469** against PG's
**2,368,670** for 1998. `LIMIT 100` masked the corruption, and the gate reported PASS for
weeks. §13.4 item 3 states the conclusion: "Any 'N queries PASS' claim carries that caveat
until the oracle grows a checksum column."

This is filed as a **task rather than a deferral** because it is a prerequisite, not a
nicety: M0125-0002's walker conversions and M0125-0004's Q75 fix both change *which rows
reach a join or a filter*, and both are accepted at the SF0.5 gate. Leaving this in the
ledger would gate the next milestone on exactly the blindness §13.4 names.

## Design

### D1. Fixture shape

Extend the git-tracked fixture `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`
from

```
q|status|rows|secs
```

to

```
q|status|rows|ck|secs
```

`rows` and `ck` must be re-derived from the **same** PG run — a checksum captured in a
different run than its row count is not a fixture, it is two fixtures.

**This forces a change of capture method, which is the substance of this task.** The current
`cmd_oracle` derives `rows` from `EXPLAIN (ANALYZE, TIMING OFF)` via `sum_top_actual_rows`.
`EXPLAIN ANALYZE` emits a **plan, not result tuples** — there is nothing to checksum in that
run. So the oracle capture must switch to a **plain execution** of each query, taking `rows`
from the `(N rows)` markers and `ck` from the tuples of that same execution.

That switch is not free and its risk is the acceptance criterion in D5: the new row counts are
derived differently from the pinned ones, so **they must be proven identical to the existing
fixture** before the new oracle replaces it. Any query where they differ is a finding about the
old capture method, not a licence to re-pin.

### D2. Float normalisation is mandatory, and the reason is already on record

Ledger row `tpcds-round2 stddev-precision` records goopg's `stddev_samp`-derived float output
differing from PG in the **last 1–2 significant digits on 235 of 236 Q39 rows** (goopg:
128-bit Newton-Raphson, formatted at 18 significant digits; PG: `sqrt_var`,
`postgres/src/backend/utils/adt/numeric.c`). A naive byte checksum would flag Q39 — and every
`stddev`/`avg`-bearing query — as corrupt on the first run.

Normalise every float to **12 significant digits** before hashing, and pin that contract in
the fixture header so a future reader cannot silently change it. Twelve digits is far above
the noise floor of the recorded divergence and far below the point where a real value defect
could hide.

### D3. `ck = n/a` is a first-class value, not a failure

A query whose `ORDER BY` is not a total order under `LIMIT` has no stable row *set* — PG and
goopg may legitimately return different 100-row samples of a larger tie group. For those,
record `ck = n/a` and keep the row-count check.

Do **not** paper over it by sorting the result before hashing: that would silently accept a
wrong *ordering*, which for a `LIMIT`-bearing TPC-DS query is itself a defect class. Record
explicitly which queries stay row-count-only, so the report can say how many PASSes were
value-verified rather than implying all of them were.

### D4. Gate semantics

The goopg side needs no new files: `cmd_sweep` already captures each query's full output into
a shell variable and never writes per-query result files, so the checksum is computed inline
from what it already has. (`tpcds-bench-compare.sh` *does* write `*_result.txt`; the SF0.5
gate does not, and this task must not assume it.)

- New verdict **`CKMISMATCH`**, kept distinct from `MISMATCH`. They have different meanings:
  `MISMATCH` is the wrong number of rows, `CKMISMATCH` is the right number of wrong rows —
  and the second is the more alarming of the two.
- The sweep summary reports how many PASSes were checksum-verified, e.g.
  `PASS=74 (61 ck-verified, 13 ck=n/a)`.
- Existing verdicts and exit codes are unchanged, so M0124-0001 and M0125's harnesses do not
  need to learn a new contract.

### D5. Acceptance — a gate for the gate

The change is only trustworthy if it catches the case that motivated it:

1. **The re-captured row counts equal the current fixture's, query for query.** This is the
   load-bearing one: it proves that switching from `EXPLAIN ANALYZE`-derived counts to
   execution-derived counts did not move the ground truth. A mismatch here blocks the task
   until explained.
2. **No query that legitimately passes acquires a spurious `CKMISMATCH`** — in particular Q39,
   the float-normalisation case, and every query recorded as `ck = n/a`.
3. **The Q75 case, stated as a hypothesis rather than a guarantee.** Re-running the pre-RC-1b
   binary *should* show Q75 as `CKMISMATCH`, because its `all_sales` CTE was computing
   1,057,469 against PG's 2,368,670. But the record establishes the corruption at the **CTE
   aggregate** level, not that the final `LIMIT 100` projection's values differed — that was
   never measured. If Q75 comes back PASS-with-matching-ck, the correct conclusion is that the
   `LIMIT` window happened to coincide, **not** that the checksum is broken. Verify the
   mechanism against the CTE probe before drawing either conclusion.

### D6. Deliverables

- `scripts/tpcds-result-checksum.sh`, committed — M0125 re-runs it per commit.
- The re-captured `oracle.txt` with the `ck` column and the normalisation contract in its
  header.
- `scripts/tpcds-sf05-regression.sh` teaching the `CKMISMATCH` verdict and the summary line.

## Non-goals

- `ci/batch/tpcds-row-anchors.csv`'s equivalent column. Same blindness, but it is a separately
  pinned CI fixture whose re-capture affects the nightly lane; ledger row from M0124-0003.
- Checksumming the SF=1 sweep as a gate. M0124-0001 may *capture* checksums opportunistically
  — its harness does write `*_result.txt` per query and engine — but SF=1 has no pinned oracle
  to compare against, so there is nothing to gate on.

## Gate

`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, the pre-commit hook, and D5's
three-part acceptance. No engine change.
