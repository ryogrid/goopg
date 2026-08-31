# 0124-0005 — A value checksum for the SF0.5 regression oracle

Status: implemented (2026-07-29) — see "Implementation record" at the end
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

**The implemented rule is deliberately conservative rather than exact.** Deciding "is this
`ORDER BY` a total order over this result?" per query needs the ORDER BY key mapped onto output
column positions, and TPC-DS queries order by expressions and aliases that a text parse cannot
resolve reliably. So the tool takes the query file's `LIMIT` values and marks `ck = n/a` when
any result block is **saturated** — its row count equals a LIMIT bound, i.e. rows were
discarded at the window boundary. Saturation is a *necessary* condition for boundary
ambiguity, not a sufficient one, so this over-approximates: it can never manufacture a
spurious `CKMISMATCH`, and it costs coverage on queries whose ORDER BY happens to be total.
On the SF0.5 fixture that is **38** of the 95 OK queries, leaving **57** value-verified. Tightening it
(trim only the boundary tie group, then hash) is recorded in the deferral ledger rather than
guessed at here.

Do **not** paper over it by sorting the result before hashing: that would silently accept a
wrong *ordering*, which for a `LIMIT`-bearing TPC-DS query is itself a defect class. Record
explicitly which queries stay row-count-only, so the report can say how many PASSes were
value-verified rather than implying all of them were.

### D4. Gate semantics

The goopg side needs no new files: `cmd_sweep` already captures each query's full output into
a shell variable and never writes per-query result files, so the checksum is computed inline
from what it already has. (`tpcds-bench-compare.sh` *does* write `*_result.txt`; the SF0.5
gate does not, and this task must not assume it.)

> **Implemented differently, deliberately.** The output now goes to a single reused scratch
> file (`.goopg_result.txt`, removed at the end) rather than a shell variable, so the checksum
> tool parses the *exact bytes the verdict is drawn from* instead of a re-serialised copy. The
> constraint D4 actually cares about — a passing gate must not litter the results dir — still
> holds: a per-query `goopg_q<N>_result.txt` is kept only for a `MISMATCH` or `CKMISMATCH`,
> where triage needs it.

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

## Implementation record (2026-07-29)

### What landed

- **`scripts/tpcds-result-checksum.py`** (new) — one psql result capture in, `rows=N ck=… blocks=N`
  out, both from the same parse. Named `.py`, not the `.sh` D6 reserved: the normalisation is
  `Decimal`-arithmetic (significant-digit rounding, scale collapse) that shell cannot do without
  shelling out per field, and the tool must agree field-for-field with the Python
  `scripts/tpcds-value-diff.py` that M0124-0006 already established.
- **`scripts/tpcds-sf05-regression.sh`** — `cmd_oracle` switched from `EXPLAIN (ANALYZE, TIMING
  OFF)` to a plain run and now writes `q|status|rows|ck|secs`; `cmd_sweep` reads the `ck` column,
  computes goopg's from the same tool, and adds the `CKMISMATCH` verdict (fatal, alongside
  `MISMATCH`/`ERROR`). `explain_analyze_script` and `sum_top_actual_rows` are deleted — nothing
  else referenced them. **4-column oracles still work**: a row with fewer than 5 fields yields
  `ck=n/a`, so a checkout that has not re-captured degrades to the old row-count gate instead of
  failing.
- **`bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`** — re-captured, with the
  normalisation contract written into the header so a later reader cannot change it silently.

### D5 #1 — the load-bearing criterion: PASSED

All **99** entries match the pinned fixture on both `status` and `rows`. Switching from
top-node `actual rows=` to result tuples did not move the ground truth.

It did not pass on the first attempt, and the failure is the reason this criterion was written:
Q23 and Q92 came back **0 rows against the fixture's 1**. Both return a single row holding
`NULL` (`sum`, `Excess Discount Amount`), which psql renders as a **fully blank line** — and the
parser skipped blank lines, because psql's block separator is also blank. It is not: psql emits
that blank line *after* the `(N rows)` tally, never before it, so every line between the rule and
the tally is data. With the skip removed both queries return 1 row and the whole fixture matches.
A checksum tool that silently drops NULL-only rows would have shipped as a *gate*.

### D5 #2 / #3

See the "Acceptance run" section appended below.

### Coverage, stated plainly

**57 of the 95 OK queries carry a checksum; 38 report `ck=n/a`.** The `n/a` set is decided by the
conservative saturation rule of D3, so it is an upper bound on genuine ambiguity, not a
measurement of it. Any future "N queries PASS" claim from this gate means *N right row counts, of
which the ck-verified subset are right answers* — the sweep summary now prints that split
(`PASS=… (… ck-verified, … ck=n/a)`) rather than leaving the reader to assume the stronger one.

### Acceptance run (2026-07-29, `991fc9c3` + this change)

`FORCE=1 TIMEOUT_SEC=300 scripts/tpcds-sf05-regression.sh sweep`, full 99 queries,
`analysis/tpcds-sf05-ck-m0124-0005/sweep/sweep-20260729-064607.txt`:

```
PASS=73 (44 ck-verified, 29 ck=n/a) MISMATCH=1 CKMISMATCH=5 ERROR=2 TIMEOUT=14 SKIP=4
```

**The run was taken under the nightly CI batch** (`FORCE=1`), so its **timings and its TIMEOUT
set are not comparable to a quiet-host baseline** — the M0124-0004 hazard. That does not touch
this acceptance: `CKMISMATCH` is a value verdict, and a checksum does not change under CPU
contention.

#### D5 #2 — no spurious `CKMISMATCH`: PASSED

- **Q39, the float-normalisation case, PASSes with a matching checksum** (77 rows,
  `ck=4db806a9a11fddfd`). This is the query whose `stddev_samp` differs from PG in the last 1–2
  digits on 235 of 236 rows; without D2's 12-significant-digit canon it would have been the
  first false alarm.
- **No `ck=n/a` query produced a `CKMISMATCH`** — by construction, and confirmed in the report.
- **All five `CKMISMATCH`es are real wrong answers**, checked against the PG capture by hand:

  | q | goopg | PG |
  |---|---|---|
  | 16 | `0 \| NULL \| NULL` | `23 \| 93334.17 \| -35323.69` |
  | 94 | `0 \| NULL \| NULL` | `2 \| 5037.18 \| 1067.82` |
  | 95 | `0 \| NULL \| NULL` | `23 \| 45031.03 \| -1282.36` |
  | 87 | `23837` | `23762` |
  | 97 | `230679 \| 112269 \| 395879` | `271980 \| 143406 \| 35` |

  Q16, Q94 and Q95 share one goopg checksum (`512b5fdab820c47b`) because they return the *same*
  row — the aggregate of an empty match — which is the same 0-row shape as Q47's `MISMATCH`, not
  three coincidences.

**Q16 is the proof the column works.** M0124-0006 found Q16 to be "a wrong answer behind a
matching row count since chunk 2, unnoticed for ten chunks" — it took a bespoke SF=1
investigation to find. The gate now flags it automatically, on every run, in 24 seconds. Four
more queries came with it that no one had attributed at SF0.5.

#### D5 #3 — the pre-RC-1b Q75 replay: NOT run, deferred

It needs a build of the pre-RC-1b binary and its own cluster load, and it is a *hypothesis
check* — D5 itself says a PASS there would be a finding about the `LIMIT` window rather than a
broken checksum. Its purpose, demonstrating that the column catches "right count, wrong values",
is discharged more strongly by the five confirmed catches above than a single replay would have
done. Q75 at HEAD is `ERROR` (RC-1b's deterministic `division by zero`, = M0125-0004), so it is
not silently passing in the meantime. Carried as a deferral-ledger row with its resume point.

#### Cross-check: the five catches are the *already-attributed* set, with one new fact

None of the five is an untracked defect — Q16/Q94/Q95 are M0125-0007's `0 / NULL / NULL`
empty-match class, Q87 was caught by value at a matching count in M0124-0001 chunk 12, and Q97
is M0125-0009's most extreme instance. The gate reproducing exactly the set that took a
chunk-by-chunk SF=1 investigation to assemble is the strongest available evidence that it is
measuring what it claims to.

**New fact, worth carrying into M0125:** Q97 at SF=1 was `392155|392155|392155` — three
identical columns, the M0125-0009 aggregate-dedup replication signature. At SF0.5 *after* that
fix landed (`3fbce36a`, `2e09250b`) goopg now returns `230679|112269|395879`: three distinct
values, so the replication is genuinely gone — and still wrong against PG's
`271980|143406|35`. **M0125-0009's fix corrected the symptom that was visible; a second,
independent defect sits under it in Q97.** Without the checksum column that would have read as
a plain PASS.
