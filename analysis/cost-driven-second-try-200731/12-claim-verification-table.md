# 12 — Claim verification table

Every load-bearing factual claim in this document set, with where it was checked. Line
numbers are as of HEAD `23a077ae` (branch `tpcds-fix2`, 2026-07-31) and may drift; the symbol
names are the durable reference.

| # | claim | evidence | verified |
| --- | --- | --- | --- |
| 1 | `joinOp` drains the **right** side and streams the left by default | `internal/executor/operators_join_agg.go:645` (`drainRowsBounded(o.right, budget)`), `:510-522` (`openProbeSide`) | read |
| 2 | The binary hash join build side **spills** | `internal/executor/spill.go:342-380` (`drainRowsBounded`), budget from `ctx.WorkMem`, default 512 MiB | read |
| 3 | MHJ's build side does **not** spill and deep-copies every row | `internal/executor/multi_hash_join.go` build loop → `drainRowsCtx`; `operators_join_agg.go:3351-3380` | read |
| 4 | `VirtualSlot.Row()` materialises a fresh `[]Datum`; `acquireRow` pools only widths ≤ 64 | `internal/executor/slot.go:159-166`; `internal/executor/row_pool.go:23,42-53` | read |
| 5 | On the **live** path the per-level materialisation is `Slot.fillFromTupleSlot`, not `slotRow(probeSlot)` — `Slot.Row()` is a zero-copy view | `internal/executor/opnode.go:110-111` (`return Row(s.Cells)`), `:129-150` (`fillFromTupleSlot`), `:868-876` (`joinOpKernelNext`) | read (review F2) |
| 5b | On the **legacy** `Build` path `nextLazy`'s `slotRow(probeSlot)` *is* the `acquireRow` site | `operators_join_agg.go`, "Pull next probe row" block in `nextLazy` | read |
| 6 | `releaseRow` is never called from `operators_join_agg.go` | `grep -rn releaseRow internal/executor/*.go` → only `operators_ddl.go`, `operators_storage.go`, `operators_index.go`, `row_pool.go`, `slot.go` (comment) | grep |
| 7 | `nextLazy` memcpy's `lazyKeyRow` per probe row | `operators_join_agg.go`, the `copy(o.lazyKeyRow[:o.lazyLW], r)` block | read |
| 8 | `Datum` is 48 bytes | `internal/executor/datum.go:119` — `const _ uintptr = 48 - unsafe.Sizeof(Datum{})` | grep |
| 9 | There are two operator builders | `Build` at `internal/executor/executor.go:21`; `BuildFast` at `:563` → `buildRec` at `:424`; `BuildFastIterator` at `internal/executor/opnode.go:395` documented as "the drop-in replacement for executor.Build in dispatch" | read |
| 10 | `buildRec` handles `*planner.Join` but routes `*planner.MultiHashJoin` to the legacy adapter | `executor.go:535-547` (Join arm), `:549-557` (default → `Build`), `:275-284` (MHJ arm in `Build`) | read |
| 11 | `maybeInstrument` keys stats on the plan node | `internal/executor/instrument.go:241-256` | read |
| 12 | `ctx.SharedHashBuilds` is keyed by `*planner.Join`; the probe-side rule is triplicated and dangerous | `internal/executor/parallel_hash_build.go:95-100`, `:104-117` | read |
| 13 | `preserveCTIDRel` is set by `lockRowsOp.Open` before the child's Open | `operators_join_agg.go:105-118` (doc comment), `:699+` (`buildHashRightWithCTID`) | read |
| 14 | Cancellation is checked per `Next()` and every 4096 build rows | `nextLazy` top; `buildLazyHashTable` `if buildCount&0xFFF == 0` (both build loops); `drainRowsCtx` every 1000 rows (`:3355`) | read |
| 15 | `mhjPackingEnabled` defaults true; `init()` forces false under cost-driven | `internal/planner/bushy.go:580`, `:17-22`, toggle at `:582-587` | read |
| 16 | `collectMultiHashTables` accepts only `*SeqScan` leaves and stops at Filter/Project/Sort/Aggregate | `bushy.go:1531-1554` | read |
| 17 | It silently drops an unresolvable key and never asserts `len(keys) == len(scans)-1` | `bushy.go:1596-1604` (`if li >= 0 && ri >= 0`), `:1620-1633` (degree ≤ 2 only) | read |
| 18 | The MHJ `keySteps` BFS `break`s on no progress, leaving tables unreached | `multi_hash_join.go:158-163`; unreached tables keep `o.nulls[i]` (`:~275`) | read |
| 19 | MHJ sorts its `Tables` by catalog OID | `bushy.go:1760+` (per cost-model doc 15 §4, which cites it) | doc 15 §4 |
| 20 | The stale-permutation wrong-rows bug is documented in the tree | `internal/planner/plan.go:869-886` | read |
| 21 | EXPLAIN prints `Multi-Way Hash Join (%d tables)` | `internal/executor/operators_explain.go:1386-1390` | read |
| 22 | Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21 were the MHJ-shaped TPC-H queries | `operators_explain.go:1562-1572` | read |
| 23 | `make plan-gate` diffs goopg against its own snapshot, and SKIPs | `Makefile:376-390`, `:340-347` (defaults `PLAN_DB=tpch`) | read |
| 24 | `pg-oracle-diff.sh` diffs **results**, not plans | `scripts/pg-oracle-diff.sh:1-44` | read |
| 25 | Spotcheck anchors are Q12=2, Q13=35 | `bench/tpch/spotcheck_expected.env` | read |
| 26 | Spotcheck names silent row-count regression as the most expensive failure mode, and SKIPs on missing data | `scripts/tpch-spotcheck.sh:5-9`, `:15-17` | read |
| 27 | `ci/batch/tpch-row-anchors.csv` has 12 anchor rows (13 lines incl. header) | `wc -l` | run |
| 28 | The TPC-DS SF0.5 oracle is 112 lines with a per-query value checksum | `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt` header lines 1-5; `wc -l` | read + run |
| 29 | The SF0.5 gate refuses to run during an SF=1 sweep | `scripts/tpcds-sf05-regression.sh:144-165` (`guard_sf1_sweep`) | read |
| 30 | `RALPH_PRECOMMIT_SCOPE` defaults to `full` | `scripts/ralph-precommit-test.sh:54-55` | read |
| 31 | The SF1 A/B files exist at the same HEAD and same day | `docs/design/cost-model/evidence/sf1-r5-{default,costdriven}-cb37d166.txt`, headers | read |
| 32 | The two A/B runs used **different** per-query timeouts (600 s vs 300 s) | headers, line 2 of each file | read |
| 33 | Completing-both totals: default 1034.04 s vs cost-driven 1160.96 s | recomputed from the two files | computed |
| 34 | Wins concentrated in Q8 (−137.03) and Q2 (−48.51); losses in Q7 (+126.01), Q10 (+99.66), Q18 (+92.02) | recomputed | computed |
| 35 | Q5/Q9/Q21 fail under cost-driven; they cost 52.20 s combined under default | recomputed | computed |
| 36 | Doc 15 concludes the blocker is join **order**, not MHJ cost; records 804 s vs 118 s and 416673 vs 393420 at `GOOPG_MAT_MULT=100` | `docs/design/cost-model/15-mhj-in-cost-driven-star-shapes.md`, status block and §2-3 | read |
| 37 | `planner.Join` has 14 fields incl. 2 unexported | `internal/planner/plan.go:826-865` | read |
| 38 | `Join.LeftKey/RightKey` are populated when `Algo == JoinAlgoHash` | `plan.go:833-834` | read |
| 39 | The int64 hash fast path is INNER-only | `operators_join_agg.go:~670-685`, `lazyHashInsertDatum` `:975-995` | read |
| 40 | `isCanonicalKeyEquality` exists and is reusable | `bushy.go:1647-1657` | read |
| 41 | `Join.Output()` returns the **cached** `n.schema` for non-semi/anti joins — a width check cannot detect a stale permutation | `internal/planner/plan.go:889-897`, comment `:869-886` | read (review F1) |
| 42 | `drainRowsBounded` **deep-copies** every retained row (`cloneRowOwned` for arena rows, else `make`+`copy`) | `internal/executor/spill.go:388-399` | read (review F5) |
| 43 | `Build` takes only a `planner.Node` — no root, no session, no `*Context` | `internal/executor/executor.go:21`, `:424` | read (review F3) |
| 44 | `Gather` builds worker trees via `Build(p.Child)`, so a `Gather`-in-plan check cannot fire inside a worker build | `internal/executor/executor.go:213-219` | read (review F4) |
| 45 | `prebuildSharedHashJoins` walks the built **operator** tree for `*joinOp` | `internal/executor/parallel_hash_build.go:119-150` | review F4 |
| 46 | A `*planner.Join` under a SubPlan is forced to `rescanCloseOpen` | `internal/executor/subplan.go:223-230` | read (review F10) |
| 47 | `instrumentScope` is a mutable package global set/restored without a lock | `internal/executor/instrument.go:215`, `:225-233` | review F8 |
| 48 | `explainOp.Open` builds via the **legacy** `Build` under `withInstrumentation` | `internal/executor/operators_explain.go:57-64` | review F6 |
| 49 | `evalHashKeyDatum` takes a `Row`, not a slot | `operators_join_agg.go:960-968` | read (review F11) |
| 50 | The merged key coordinate space is not universal: `unnest.go:2107` builds an INNER hash join with `RightKey.Index = 0`; `bushy.go:1391-1396` shifts by `len(leftSchema)`; `reresolveJoinByName` repairs late (`bushy.go:2902-2925`) | as cited | review F9 |
| 51 | `MultiHashJoin` has live references across ~20 files, incl. `generateMultiHashJoinPath` (`pathgen.go:100-105`) | as cited in [08 R17](08-risk-register.md) | review F13 |

## Claims deliberately NOT verified (and why)

| claim | status |
| --- | --- |
| the exact wall-clock benefit of Stage 0a/0b | **unmeasured by design** — measuring requires starting a server, which was out of scope for this analysis (a TPC-DS sweep owns the host). Stage 0c exists to measure it. |
| that `Join.LeftKey.Index` is in the merged coordinate space | **inferred** from `nextLazy` building `lazyKeyRow` at `leftWidth+rightWidth` before evaluating. [05 Q3](05-qualification-predicate.md) makes this an explicit Stage-1 test rather than an assumption. |
| that no other structural goopg-vs-PG EXPLAIN differences remain after MHJ | **explicitly assumed false** — [06 §4](06-explain-and-plan-shape.md) lists candidates and mandates report mode first. |
| the 804 s / 118 s Q9 pair | taken from doc 15's status block; it is a different measurement family from the `sf1-r5-*` files and the two are never mixed in one table here. |

---

## Synthesis-pass verifications (2026-07-31)

Checks run by the merge pass, not by either design pass — either to resolve a disagreement
between them or because a claim unique to one pass was load-bearing enough to warrant a second
confirmation. Full transcript with commands and output:
[evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt).

| id | claim | how checked | result |
| --- | --- | --- | --- |
| V1/V7 | The MHJ-shaped TPC-H set at this HEAD is `{Q2,Q3,Q7,Q9,Q10,Q11,Q18,Q21}`; **Q5 is not in it** | attribute every `Multi-Way Hash Join` line in `plan_snapshots/m0125-0043-after.txt` to its `=== Qn` header | **CONFIRMED.** Falsifies the "MHJ-shaped ⇒ collapses" partition in both directions: Q5 (no MHJ) is the worst regression; Q2/Q3/Q11 (MHJ-shaped) get faster |
| V2 | goopg emits no bare `Hash` node where PG always does | `grep -cE '^\s*->\s+Hash\s*(\(\|$)'` vs `'-> Hash Join'` on the same snapshot | **CONFIRMED** — 0 vs 40 |
| V3 | `EstimateRows` has no `*MultiHashJoin` arm | enumerate `case *` in `internal/planner/cardinality.go:37-90` | **CONFIRMED** — 15 arms, none for `MultiHashJoin`. Severity raised to a live default-config defect ([08 R18](08-risk-register.md)) |
| V4 | `VirtualSlot.Materialize()` does not clone arena-backed Datums | read `internal/executor/slot.go:155-175` | **CONFIRMED** — `Materialize()` → `Row()` → `acquireRow` + `Get(i)`, no `cloneRowOwned` |
| V5 | `drainRowsBounded` deep-copies and the copy is required | read `internal/executor/spill.go:384-402` incl. the M0073-0004 comment | **CONFIRMED** — `cloneRowOwned` when `rowHasArena`, else `make`+`copy` |
| V6 | The MHJ-drop regression is already recorded in-repo | read `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md:188-198` | **CONFIRMED verbatim**, including its own lesson that the direction is not predictable and must be measured per commit |
| V8 | `buildRec` does not migrate `Aggregate` to the slab | enumerate `case *planner.` arms in `internal/executor/executor.go:425-555` | **CONFIRMED** — `SeqScan Filter Project Limit Sort Update Delete Insert Join` only |
| V9 | build-side selection is size-based with a small-dimension override | read `internal/planner/bushy.go:1378-1386` | **CONFIRMED** |
| V10 | 28 non-test `case *MultiHashJoin:` arms across 15 files | `grep -rn … internal/ --include=*.go \| grep -v _test.go \| wc -l` and `grep -rln … \| wc -l` | **CONFIRMED** — exactly 28 / 15 |
| V11 | The SF0.5 oracle content-verifies 57 of 99 queries | parse `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt` (`q\|status\|rows\|ck\|secs`) | **CONFIRMED** — 99 rows, 57 real checksums, 42 `ck=n/a`; statuses 95 OK / 3 SKIP_QUERYGEN / 1 TIMEOUT |
| V12 | goopg EXPLAIN prints placeholder costs and widths | `grep -c 'cost=0.00..0.00'` and `grep -c 'width=0'` on the snapshot | **CONFIRMED** — 204 of each |

### Standing caveat on V1/V7

`plan_snapshots/m0125-0043-after.txt` is from 2026-07-31; the A/B evidence it is cross-referenced
against is from `cb37d166` on 2026-07-24. Plans may have shifted between the two dates. The
conclusion (the partition does not hold; order quality is the variable) is robust to that,
because it is falsified in both directions and by a query with no MHJ at all — but the **query
set itself must be re-derived at the measurement HEAD**, never hard-coded. This is the same
discipline [09](09-staged-implementation-plan.md) Stage 0c imposes.
