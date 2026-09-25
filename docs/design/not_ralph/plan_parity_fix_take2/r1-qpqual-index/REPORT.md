# R1 results — qpqual on the index path

*Round 1 of `../TODO.md`. Design: `DESIGN.md` (committed `e9c00d427`).
Implemented, gated and measured 2026-09-08 on branch
`plan-parity-with-pg-take2`.*

## 1. Verdict

**The change is correct, the values are unmoved, and plan parity did not
improve on either corpus.** R1 charges PG's qpqual currency at the index
sites; it repriced 1 TPC-H plan and 33 TPC-DS plans, and moved the
match count by **zero** on both. That is the round's finding, and it is
reported as the primary result rather than buried under the green gates.

| channel | before (R0) | after (R1) |
|---|---|---|
| TPC-H shape parity (22 sections) | match=1 shapediff=12 missingnode=9 | match=1 shapediff=12 missingnode=9 |
| TPC-DS shape parity (99 sections) | match=0 shapediff=34 missingnode=62 error=3 | match=0 shapediff=34 missingnode=62 error=3 |
| TPC-H plans repriced | — | 1 (Q12) |
| TPC-DS plans moved | — | 33 real (36 reported, 3 are the Q36/70/86 parse-error blocks) |
| TPC-H values (22 queries) | baseline | **22/22 byte-identical** |
| TPC-DS SF0.5 values | baseline | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |

Both values gates are the all-zero result the round required. Under the
goal's timing rule (same plan ⇒ a timing delta is not a regression) no
timing adjudication applies: on TPC-H no shape moved at all.

## 2. What landed

The design's five sites, unchanged in scope:
`costIndexScanCore` (`costindex.go`), `costBitmapHeapScan`
(`costbitmap.go`) and its two estimators, the ordered
(`pathindexordered.go`), index-only (`pathindexonly.go`), bitmap
(`pathbitmap.go`) and parameterised (`pathparamindex.go`) producers.
`planner.go`'s rule-based chooser passes 0, as designed — no `addPath`
comparison exists there.

The index side now pays `(cpu_tuple_cost + cpu_operator_cost x
numQualOps) x tuples_fetched`, the identical expression `costSeqscan`
evaluates per tuple scanned. `costindex.go:229-247`'s own comment — which
named this defect and named this follow-up — is discharged.

### 2.1 A defect found reviewing the design's own implementation

The parameterised site shipped as

```go
numQualOps: localQualOpCount(rel.baseLeaf) - float64(len(clauses)),
```

which **subtracts a join-clause count from a local-restriction count**.
The two are disjoint populations: `clauses` is drawn from `bound`, which
comes from `indexableJoinClausesFor(...)`, while `localQualOpCount`
counts the leaf's `Filter` chain. For the common shape — a rel with no
local filter probed on one join clause — it yields **-1**, i.e. a
*negative* qual charge that CREDITS the index path with more than the
asymmetry R1 exists to remove.

PG's rule is `baserestrictinfo + ppi_clauses` minus the index quals
(`costsize.c:806-820`). Here that is every local conjunct (none is an
index qual on this path) **plus** the movable join clauses the probe does
not bind. Extracted as `paramIndexQualOpCount(leaf, nBound, nIndexQuals)`
and floored at zero; pinned by
`TestParamIndexQualOpCountAddsPopulations`, whose first case is exactly
the -1 shape.

## 3. Why parity did not move

R1's charge is real and it reaches production — instrumentation printed
`n=5` for TPC-H Q12's `lineitem` leaf, and Q12's plan cost rose by
75,015.66 = 5 x `cpu_operator_cost` x 6,001,255, to the cent. The charge
is not inert. It simply does not change **which** path wins, anywhere.

Two structural observations from the instrumented run, both new and both
larger than R1:

**(a) The scan in the winning plan is not priced by any R1 site.** Q12's
plan is a Merge Join whose inner is
`Index Scan using idx_lineitem_orderkey_fkidx (cost=0.00..60475.14
rows=46259)`. That node's printed cost is **byte-identical before and
after R1**, while the Merge Join above it rose by exactly the qpqual
charge. A `cost=0.00` startup is not `costIndexScanCore` output (which
always charges a B-tree descent). The base-rel seed is a `PathPrebuilt`
wrapping the **pre-search leaf** (`joinsearch.go:434`), priced by
`costSeqscan` on `baseSeqScanCostInputs` — which returns
`numQualOps = 0` and fallback pages for any non-`*SeqScan` leaf (K2).
So goopg is displaying one cost for that node and costing the join with
another. This is R2's subject and it is now evidenced, not inferred.

**(b) The parity comparator cannot read most of the corpus.** Of the 62
TPC-DS `MISSING-NODE` verdicts, **56 cite `unknown node kind`**; on
TPC-H it is 9 of 9. The unknown vocabulary is
`Finalize/Partial {Hash,Group,}Aggregate`, `WindowAgg`, `CTE`, `SetOp`,
`HashSetOp`, `Merge`. A `MISSING-NODE` verdict therefore does **not**
mean goopg omitted a node — in the majority of cases it means the tool
did not recognise a node name that both engines emit.

Consequence for the goal: **the current instrument cannot prove plan
identity.** Until it can, "match=0 on TPC-DS" is an unknown, not a
measurement — a query could already be planning identically and be filed
as `MISSING-NODE`. Teaching the comparator the node names both engines
already print is a fix to the *measurement*, not to the plans; it forces
nothing to be equal, and it can only ever reclassify a verdict the tool
was guessing at. It is therefore promoted ahead of the remaining costing
rounds (new R2 in `../TODO.md`).

## 4. A contaminated arm, caught and discarded

The first R1 capture reported "zero plans changed on both corpora" — and
was **measuring the R0 binary**. `/tmp/parity-r0/launch.sh` decides
readiness with `pg_isready`, which was answered by a *surviving R0
server* on :5543; the new instance never bound the port, the launcher
printed `READY :5543 after ~2s`, and the arm looked entirely normal. It
was only caught because an instrumented build produced a Q12 cost the
"R1" capture did not have.

This is the documented failure class (`goopg_shared_bench_cluster_collisions`):
a benchmark arm that ran against the wrong binary produces a plausible
number with no error. All R1 numbers above come from re-captures taken
after the fix.

`launch-verified.sh` (in this directory) is the remedy and is now the
required launcher for every round: it stops the datadir's server, proves
the port stops answering, starts, then proves via `/proc/<pid>/exe` that
the listener's binary **inode** is the one we built, and refuses
otherwise. Every arm in this report printed
`VERIFIED :<port> pid=<n> exe=<path>`.

## 5. Gates

- `go test ./internal/optimizer/` — green (incl. 3 new pins:
  `TestCostIndexScanQpqualCurrency`, `TestCostBitmapHeapScanQpqualCurrency`,
  `TestLocalQualOpCountMirrorsSeqRivalCount`,
  `TestParamIndexQualOpCountAddsPopulations`).
- `go test ./internal/executor/` — green.
- TPC-H values A/B on one private clone, both binaries, verified
  identity: `values-tpch-r0.txt` vs `values-tpch-r1.txt`, byte-identical.
- TPC-DS SF0.5 sweep vs the git-tracked PG oracle:
  `values-tpcds-sf05-r1.txt`, `PASS=95 ... SKIP=4`, all failure counters 0.

## 6. Evidence in this directory

`tpch-goopg.sections.txt`, `tpch-diff.txt`, `tpcds-goopg.plans.txt`,
`tpcds-shape-diff.txt` (R1) and `tpcds-shape-diff-r0.txt` (R0, newly
computed — R0 recorded only byte-equality for TPC-DS, which can never
match because costs differ; the shape verdict is the comparable channel
and is added retroactively for both arms), `values-*`, `launch-verified.sh`.

## 7. Filed, not bundled

- **Index-qual operator cost** (PG's `index_qual_cost` on index tuples)
  is still uncharged — DESIGN §5's suspect #1. R1's outcome neither
  confirms nor clears it: the winning scans are not priced here at all
  (§3a), so the suspect cannot be tested until R2 lands.
- **Seq-side qpqual startup**: unchanged, still needs a corpus qual that
  accrues startup.
- **EXPLAIN prints a stale cost for a join's inner index scan** (§3a) —
  a reporting defect independent of costing; worth a ledger row.
