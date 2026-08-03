# M0125-0002 commit 6 — `conjunctIsLocalEligible` + `localizeExprToLeaf`

Date: 2026-08-03. Branch `planner-dp-and-related-refactor`, before-commit
`53e46eaf` (commit 5).

Commit 6 is the first PAIR in the series and the first whose producer fails
**OPEN**: `conjunctIsLocalEligible` returned a vacuous `true` for any conjunct
built from kinds its 9-arm switch never enumerated, so completing it can
*remove* predicates from the leaf-local set. D2 row 6 sizes the TPC-H blast
radius at {Q2, Q5, Q7, Q8, Q9} — the ≥5-FROM queries that pass
`shouldAttachBeforeMHJ`'s `region`/`nation` SmallDimension clause.

## Verdict

**No-op on both benchmarks, proven three ways.** D2 row 6's shape-move
prediction is REFUTED by measurement.

| instrument | result |
|---|---|
| TPC-H plan A/B (22 queries, `cmd/plan-snapshot`) | **22/22 byte-identical** |
| TPC-DS SF0.5 `EXPLAIN` A/B (96 queries) | **96/96 byte-identical** (`diff -rq` clean) |
| divergence probe, 118 planned queries | **0 `C6ELIG` / 0 `C6LOC` / 0 `C6ABORT`** over 277 eligibility calls + 175 localization calls |

The before arm reproduced `plan_snapshots/m0125-0002-c5-after.txt`
byte-for-byte, so the instrument is stable across loops and the A/B is a
same-cluster comparison rather than a diff against a stale label.

## Why a probe was mandatory here, not optional

Two independent blind spots, and the plan diff only closes the first:

1. **Eligibility** (which conjuncts become leaf-locals) IS visible — a
   `Filter` above a leaf scan appears or disappears in the plan text.
2. **Localization** (`ColumnRef.Index -= binding.offset`) is **invisible**.
   goopg's `EXPLAIN` prints column NAMES, not indices (M0125-0042), so a
   container the old switch returned un-rebased renders identically while the
   executor reads a different slot. The probe compares the two localized trees
   by `exprIdentityKey`, which includes `Index` — that is the whole point.

`C6CALL` / `C6LOCC` are positive controls: the sites were reached 277 and 175
times respectively, so the zeros are not vacuous.

## The latent defect this closes

`conjunctIsLocalEligible` and `localizeExprToLeaf` were incomplete in the SAME
direction, which is why the pair stayed latent: the producer usually declined
what the consumer could not rebase. Usually, not always. `WHERE t.a IS NULL` on
a binding with `offset > 0`, inside a query that passes
`shouldAttachBeforeMHJ`, was:

- judged **eligible** (the old switch never descended `*IsNullExpr`, so the
  walk produced zero callbacks and `eligible` stayed `true`);
- moved out of `joinConjuncts` into `locals.byBinding` by
  `partitionConjunctsForJoinPlanning`;
- returned **unchanged** by `localizeExprToLeaf` (its trailing pass-through was
  a claim about the 7 kinds it knew and a silent lie about the other 25);
- attached to a leaf `Filter` still carrying FROM-cumulative indices — i.e.
  **reading the wrong column**.

Commit 4 widened the reachability rather than creating it: completing
`tableForCol` means `t.a IS NULL` now attributes to a binding instead of
answering −1, so more conjuncts reach this pair than before. The probe says
zero instances are live on TPC-H or TPC-DS SF0.5 today.

## Artefacts

| path | what |
|---|---|
| `capture-tpch.sh` | one TPC-H arm per binary, fresh capped server on :65433 |
| `capture-plans.sh` | one TPC-DS SF0.5 `EXPLAIN` arm per binary on :65437 |
| `probe-source.md` | the probe, verbatim, with the two-call-site patch |
| `before/`, `after/`, `probe/` | 96 per-query SF0.5 plans per arm (+ `.meta`) |
| `*.server.log` | server logs; the probe's counters are grepped from these |
| `plan_snapshots/m0125-0002-c6-{before,after,probe}.txt` | TPC-H arms |

Counters (grep the exact prefixes — `C6LOCC` contains `C6LOC` as a substring
and a lazy `grep -c C6LOC` reports the positive control as a delta):

```
grep -c 'C6CALL n='   <log>   # 43 TPC-H + 234 TPC-DS = 277
grep -c 'C6ELIG delta=' <log> # 0
grep -c 'C6LOCC n='   <log>   # 22 TPC-H + 153 TPC-DS = 175
grep -c 'C6LOC delta=' <log>  # 0
grep -c 'C6ABORT n='  <log>   # 0
```

## Gates

units precommit PASS; full `internal/planner` package green; census gate
(`TestExprSwitchInventoryIsPinned`) green with `conjunctIsLocalEligible`
DEMOTED to `nonRecursiveClassifier` and `localizeExprToLeaf` DELETED;
48 new pin subtests proved to FAIL against the old bodies before passing
against the new ones; `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2 rows
23.1 s, Q13=35 rows 11.3 s); pgbench smoke via the commit hook.

Timed 22-query TPC-H power run and the SF0.5 answer sweep: **not run** — see
the deferral-ledger row dated 2026-08-03, which also converts the four
consecutive per-commit skips into ONE cumulative timed run owed at commit 8.
