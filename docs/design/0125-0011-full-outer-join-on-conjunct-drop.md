# M0125-0011 — merge join dropped every ON conjunct after the first

*Filed 2026-07-29 (discovered by M0125-0009's acceptance run). Fixed 2026-07-29.*

## Summary

`runMergeJoin` joined purely on the single merge key and **never evaluated
`Join.Predicate`**. Because the planner's `splitEqualityForHash` returns only the
*first* usable equality conjunct of the `ON` clause and discards the rest, every
remaining conjunct — equality or not — was silently dropped at execution time.

goopg selects `JoinAlgoMerge` for `RIGHT` and `FULL` joins, so those two join
types produced a **cross product within each equal-key group** instead of a
filtered join, and additionally lost the null-extended rows that the rejected
residual should have produced.

The headline symptom, from TPC-DS Q97 at SF=1:

| probe (`ssci` / `csci` are Q97's two CTEs) | goopg (pre-fix) | PostgreSQL 18.3 |
|---|---|---|
| `count(*)` of each CTE | `548694 / 287769` | `548694 / 287769` |
| `ssci JOIN csci ON (customer_sk AND item_sk)` | `161` | `161` |
| `ssci FULL OUTER JOIN csci ON (customer_sk)` | `2131274` | `2131274` |
| `ssci FULL OUTER JOIN csci ON (customer_sk AND item_sk)` | **`2131274`** | **`836302`** |

The two-conjunct FULL OUTER JOIN returned *precisely* the single-key number,
which is the signature of a dropped conjunct rather than a mis-evaluated one.
PG's `836302` is `548694 + 287769 − 161`, the full-outer identity for 161
matches, independently confirming the reference side.

## Root cause

Two cooperating pieces, each individually reasonable:

1. `internal/planner/planner.go:splitEqualityForHash` scans the conjuncts of the
   `ON` predicate and **returns on the first** one whose two sides land on
   opposite join inputs. That single pair becomes `Join.LeftKey` /
   `Join.RightKey`. This is correct as far as it goes — it is choosing *a* key,
   not claiming the predicate is exhausted. The full `ON` clause is preserved
   separately in `Join.Predicate`.

2. `internal/executor/operators_join_agg.go:runMergeJoin` consumed `LeftKey` /
   `RightKey` via `buildMergeSide` and then emitted the full cartesian product of
   each equal-key group:

   ```go
   for a := li; a < i; a++ {
       for b := rj; b < j; b++ {
           o.rows = append(o.rows, concatRows(leftKeyed[a].row, rightKeyed[b].row))
       }
   }
   ```

   `o.joinPredicateMatch` — which every other join path calls — appears nowhere
   in the function. `Join.Predicate` was simply never read on the merge path.

This is another instance of Hard-won Rule #2 (sibling paths must change
together): the nested-loop path (`runNestedLoop`) and the lateral path
(`openLateral`) both call `joinPredicateMatch`; the hash path applies the
residual too, which is why the INNER probe above was already correct. Only the
merge sibling was missing it.

## PostgreSQL's behaviour

`postgres/src/backend/executor/nodeMergejoin.c` keeps the two concerns separate
and applies **both**:

- `mergeclauses` locate the equal-key group (`MJCompare`), and PG supports a
  *list* of them, not one;
- `EXEC_MJ_JOINTUPLES` then runs `ExecQual(joinqual)` on each candidate pair
  inside that group;
- when the joinqual rejects a pair, the `MJ_FILL_OUTER` / `MJ_FILL_INNER` states
  emit the null-extended row for the unmatched side.

goopg implemented the first bullet only.

## The fix

`runMergeJoin`'s equal-key group now mirrors PG's three steps. For each
candidate pair the full `Join.Predicate` is evaluated via the existing
`joinPredicateMatch`; per-row `leftMatched` / `rightMatched` flags record which
rows found a partner that *survived the residual*; and rows that did not are
null-extended according to join type before the merge advances.

The critical semantic point is the third step. The pre-existing `cmp < 0` /
`cmp > 0` arms only null-extend rows whose **key** found no partner. A row whose
key matched but whose residual failed is equally unmatched for outer-join
purposes, and nothing in the old structure could emit it — which is why the
pre-fix FULL OUTER JOIN was missing rows as well as inventing them.

No allocation is added on the matched path: `concatRows` was already being
called for every pair; it now happens before the predicate test instead of
inside the `append`. Pairs the residual rejects now *skip* their `append`, so a
selective residual makes the path cheaper, not more expensive.

## Scope of the bug

Wider than the filed report, which named FULL OUTER JOIN only:

- **FULL** and **RIGHT** joins were affected in normal operation — these are the
  two types the planner routes to `JoinAlgoMerge`.
- **LEFT** and **INNER** are unaffected in practice (they route to hash or
  nested loop) but were equally broken *if* a merge plan was constructed
  directly, which the new unit test exercises for all four types.

## Verification

**Differential vs PostgreSQL 18.3** — 24 combinations (4 join types × 6 `ON`
shapes: two-conjunct equality, three-conjunct equality, equality + `>`,
equality + `<>`, equality + `IS NOT NULL`, and single-conjunct control), over a
fixture containing duplicate keys, NULL join keys, and NULL residual columns.
All 24 matched exactly; before the fix the multi-conjunct shapes diverged.

**Unit regression** — `TestExecMergeJoinAppliesResidualConjuncts`
(`internal/executor/merge_join_residual_test.go`). Expected row sets are the
*measured* PG answers, not hand-derived. Its last subtest encodes the fix_plan's
acceptance criterion directly: a two-key FULL OUTER JOIN must not collapse onto
its single-key counterpart. Verified to fail on all five subtests before the fix
and pass after.

**TPC-DS Q97 at SF=1** — goopg `541140|286927|161`, identical to PG; the full
probe matrix above now matches on all five rows.

## The SF0.5 gate does see this defect — and a trap that hid it

**Full SF0.5 sweep, 99 queries, diffed per query against the pre-fix baseline
(`analysis/tpcds-sf05-ck-m0124-0005/sweep/sweep-20260729-064607.txt`):**

```
baseline: PASS=73 (44 ck-verified, 29 ck=n/a) MISMATCH=1 CKMISMATCH=5 ERROR=2 TIMEOUT=14 SKIP=4
post-fix: PASS=74 (45 ck-verified, 29 ck=n/a) MISMATCH=1 CKMISMATCH=4 ERROR=2 TIMEOUT=14 SKIP=4

per-query status changes: Q97: CKMISMATCH -> PASS      (the only one, of 99)
```

Confirmed by a direct A/B on the same harness, each arm with an explicitly
rebuilt binary:

| binary | Q97 at SF0.5 |
|---|---|
| pre-fix | `CKMISMATCH ck=5687f61d9fdd4f93` (oracle `65725195ebe13a3b`) |
| post-fix | `PASS ck=65725195ebe13a3b` |

So the gate is sensitive to this defect, the fix is its only effect across 99
queries, and **no Q97 anchor re-pinning is needed** — the oracle was already
right and goopg has moved onto it.

### The trap: the sweep path never rebuilds the bench binary

This nearly got recorded as the opposite conclusion. An earlier probe of
`QUERIES="97" … sweep` appeared to **pass with the fix reverted**, which would
have meant the gate was blind. It was an artifact:

```sh
# scripts/tpcds-sf05-regression.sh:256 — inside load-goopg, NOT the sweep
[[ -x "${GOOPG_BIN}" ]] || ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )
```

`GOOPG_BIN` is `tmp/goopg-bench-bin`, and the sweep builds it **only when it is
missing**. Reverting a source file therefore changes nothing about what the
sweep executes: it silently measures whatever binary was last left in `tmp/`,
which may be from an unrelated session or from the nightly batch. (`bench/tpcds/server.sh`
*does* rebuild unconditionally, so which entry point started the server decides
whether your change is even under test.)

The rule this implies, and the reason it is recorded rather than assumed away:
**a source-level A/B against the SF0.5 sweep is meaningless without an explicit
`go build -o tmp/goopg-bench-bin ./cmd/goopg` in each arm.** A green sweep after
an edit does not by itself prove the edit was exercised. Filed as a
deferral-ledger row (2026-07-29) proposing the sweep stamp the binary's
build-id/mtime into its report header so a stale-binary run is visible in the
artefact rather than inferable only from process archaeology.

## Deferred

`Join` carries a single `LeftKey`/`RightKey` pair, whereas PG's merge join takes
a list of mergeclauses. Post-fix goopg is *correct* for multi-key joins — the
extra keys are enforced as residual — but it is **slower** than PG, because the
equal-key group is formed on one column and the rest are filtered per pair. On
Q97 that means grouping on `customer_sk` alone and filtering `item_sk`. Making
`splitEqualityForHash` return every disjoint-side equality and teaching
`buildMergeSide` a composite sort key would restore PG's selectivity. Recorded
in `.ralph/deferral_ledger.md` (2026-07-29, M0125-0011); resume point is
`splitEqualityForHash` plus `mergeKeyedRow.key`.

## Files

- `internal/executor/operators_join_agg.go` — `runMergeJoin` equal-key group.
- `internal/executor/merge_join_residual_test.go` — new regression test.
