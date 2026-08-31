# root-0041 — A partial index was selectable for quals its predicate never implied

status: accepted
date: 2026-08-07
area: planner / index selection
supersedes: none
related: root-0040 (the wedge that hid this), `.ralph/deferral_ledger.md`

## Summary

goopg's main scan path chose partial indexes without ever checking that the
query's quals imply the index predicate, so a partial index silently returned
**zero rows** for any qual outside its predicate. Measured on a live server
before the fix:

```sql
CREATE INDEX onek2_u1_prtl ON onek2 (unique1) WHERE unique1 < 20 OR unique1 > 980;
EXPLAIN (COSTS OFF) SELECT unique1 FROM onek2 WHERE unique1 = 50;
--  Index Only Scan using onek2_u1_prtl on onek2
SELECT count(*) FROM onek2 WHERE unique1 = 50;   -- 0, must be 1
```

This is a wrong-answer bug, not a plan-shape one: the row exists, the scan
simply never visits it.

## How it was found

It was the **last thing standing between the tree and a clean nightly**, which
is M0127's S7 gate ("S5-ON survives a clean nightly cycle").

Nightly `20260806-232940` (sha `dffb05be`, the root-0040 latch fix) went from
10 action items to 2, with every stage — `preflight`/`units`/`race`/`testport`/
`pgbench`/`tpch`/`tpcds` — PASS and, for the first time in ten runs, no
`regress/suite-wedge` item. The two survivors were `regress/portals_p2` and
`regress/select`, both flagged "output mismatch; normalization rules need
extension", and both had defeated three earlier loops because **each passes
standalone**. Prior loops recorded `portals_p2` as "never reproduced in
isolation at HEAD" and moved on.

The order dependence was the symptom, not the cause. `onek2`'s three partial
indexes are created by `create_index`, which only runs in a full-suite pass —
so standalone the cases seq-scan and are correct, and in-suite they hit an
index that drops their rows. The harness could already dump the offending
output (`GOOPG_REGRESS_DIFF_DIR`, `internal/testport/framework/regress.go`),
but the nightly never sets it, so the divergence reached the report as one
unactionable line. Setting it locally reduced both cases to the same shape:
every diverging block is `(0 rows)` where a row was expected, and every one
queries `onek2`.

Two upstream index definitions account for both cases:

| case | query | index it wrongly used | predicate |
|---|---|---|---|
| `portals_p2` | `FETCH` on cursor over `onek2 WHERE unique1 = 50` | `onek2_u1_prtl` | `unique1 < 20 OR unique1 > 980` |
| `select` | `onek2 WHERE unique2 = 11 AND stringu1 < 'B'` | `onek2_stu1_prtl` | `stringu1 >= 'J' AND stringu1 < 'K'` |

Both were reproduced directly on a throwaway server, along with the two
controls that bound the defect: a partial index whose predicate *is* implied
returns the right row, and the scan machinery itself is sound. So the fault is
purely in **candidate selection**, not in partial-index maintenance or scanning.

## Root cause

PG builds index paths for a partial index only after proving its predicate from
the query's restriction clauses: `check_index_predicates()`
(`src/backend/optimizer/path/indxpath.c`) sets `index->predOK` via
`predicate_implied_by()`, and `create_index_paths()` skips the index when it is
false.

goopg has no predicate-implication prover. It had already reached the right
conclusion in one place — `addOneOrderedIndexPath`
(`internal/planner/pathindexordered.go`) declines `idx.HasPredicate` outright,
with a comment saying exactly why — but **the guard was never mirrored onto the
main scan path**. `HasPredicate` had zero readers in `internal/planner`'s
plain-scan selection: `findBTreeIndexForColumn` (`planner.go`) filtered on
access method and leading column only, and `pickIndexCoveringAllLeadingColumns`
(`nl_index_join.go`) on access method and column coverage only.

This is the project's recurring sibling-path failure
(`pattern_sibling_paths_must_agree`): one twin got the guard, the other did not,
and the guarded twin's passing tests said nothing about the exposed one.

## The fix

Mirror the existing rule, conservatively: decline any index with a predicate.

- `findBTreeIndexForColumn` (`internal/planner/planner.go`) — covers the plain
  scan path (`planIndexScanFromWhere`, `tryRangeIndexScan`) and all three
  multi-hash-join rewrite sites in `mhj_input_rewrite.go`, which route through it.
- `pickIndexCoveringAllLeadingColumns` (`internal/planner/nl_index_join.go`) —
  covers the parameterized inner-side path (`addOneParameterizedIndexPath`),
  a separate candidate loop that needed the guard independently.

`continue`, not `return nil`: a partial candidate must be skipped, not abandon
the search, so a plain index on the same column still wins.

This trades a plan-quality loss (goopg now seq-scans where PG uses the partial
index legitimately, as in `select`'s block) for correctness. The regress
comparison normalizes EXPLAIN bodies away, so the change costs no output parity.

## Guards

`internal/planner/partial_index_predicate_test.go`:

- `TestPartialIndexNotChosenForUnprovenQual` — both selection functions decline
  a partial index. **Non-vacuous**: neutering the two guards fails it.
- `TestNonPartialIndexStillChosen` — the guard keys on `HasPredicate`, so the
  fix cannot "pass" by disabling index scans wholesale.
- `TestPlainIndexPreferredOverPartial` — partial registered first, plain index
  still chosen; pins `continue` against a `return nil` regression.

## Verification

- Full `TestPort_RegressSuite` in real suite order: `portals_p2` and `select`
  both PASS (197 s). Diff-set comparison before/after shows 89 → 87 diverging
  cases; `hash_index` also recovered.
- units gate PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35) — the
  mandatory row-count tripwire for a planner change.
- Evidence retained under `analysis/m0127-s7-regress/order-dep-20260807/`
  (`before` = the captured divergences, `after/` = the post-fix set).

## Deferred (ledger rows)

1. **No predicate-implication prover.** goopg declines every partial index
   instead of using it when the qual implies the predicate, as PG does. Resume
   point: port `predicate_implied_by()` and gate the two call sites on it
   instead of on `HasPredicate`.
2. **`regress/truncate` is nondeterministic**, independent of this fix: its FK
   `DETAIL:`/`HINT:` lines come out in a varying order (1 of 3 standalone runs
   diverged, with a `trunc_b` line where `trunc_d` was expected). It will make
   a nightly `fail` at random until the enumeration is ordered.
3. **The nightly discards regress diffs.** `GOOPG_REGRESS_DIFF_DIR` exists but
   the nightly stage does not set it, which is why this defect survived three
   loops as "does not reproduce in isolation". Resume point:
   `ci/batch/stages/stage-testport.sh`, point it at the run dir.
