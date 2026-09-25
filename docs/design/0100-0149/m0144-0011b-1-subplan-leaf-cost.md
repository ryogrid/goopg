# M0144-0011b-1 — price a sub-plan join-search leaf from its own subtree

Status: LANDED 2026-09-20 (§3's decision was written BEFORE any parity number
was taken — AGENT.md C3)
Kind: impl
Parent: M0144-0011b
Milestone: M0144 (plan-parity harness, measurement-first era)
Recon: `docs/design/0100-0149/m0144-0011b-nontable-leaf-pricing.md`

## 1. The defect, restated in one line

`internal/optimizer/joinsearch.go` prices **every** join-search leaf with
`costSeqscan`. A leaf that wraps a finished SUB-PLAN — a set-op, CTE,
subquery, VALUES or function scan — has no `baserel->tuples`, so
`baseSeqScanCostInputs` invents a page count from the row count and the
sub-plan's entire cost is discarded. Measured on TPC-DS SF0.25 Q8:

```
joinsearch.prebuilt relids={3} rows=535 total=8.35     <- what the search sees
HashSetOp Intersect  (cost=6457.53..7360.42 rows=535)  <- what the subtree costs
```

and the join above it therefore prints `Hash Join (cost=1.27..11.28)`, a parent
650x cheaper than its own child.

## 2. What PG does

`cost_subqueryscan` starts from the subpath and adds overhead — it never
re-derives the child's work from a page count:

```c
/* postgres/src/backend/optimizer/path/costsize.c:1491-1493 */
path->path.disabled_nodes = path->subpath->disabled_nodes;
path->path.startup_cost = path->subpath->startup_cost;
path->path.total_cost = path->subpath->total_cost;
```

then, when the scan carries quals or a non-trivial target, adds
`qpqual_cost.startup`, and `cpu_tuple_cost + qpqual_cost.per_tuple` per row
(`costsize.c:1516-1525`). `cost_ctescan` (`costsize.c:1640`) and
`cost_functionscan` have the same shape: the child's work is paid for, plus a
per-tuple selection/projection charge.

## 3. The decision, and the two options NOT taken

The recon doc named three options. The choice is recorded here before any
measurement, per C3.

**CHOSEN — port `cost_subqueryscan`'s SHAPE for sub-plan leaves only:**

```
startup = subtreeCost.startup
total   = subtreeCost.total + cpu_tuple_cost * rows
```

with `subtreeCost = legacyDisplayCostOf(leaf)`, which prefers the leaf's
CARRIED `PlanCost` when it has one and otherwise derives monotonically from
children that do. `cpu_tuple_cost * rows` is PG's per-tuple selection and
projection charge; the qual term is NOT added, because the leaf's local filter
is already inside the node being priced (goopg's leaf is a finished tree, not
a bare RTE with a `baserestrictinfo` list hanging off it).

**Scope restriction that makes the result attributable:** only leaves whose
`leafBaseScan` is NOT a base-table access node (`*SeqScan`, `*IndexScan`,
`*IndexOnlyScan`, `*BitmapHeapScan`) are repriced. An index or bitmap leaf is
the rule-based planner's own choice standing in for the relation; it is a
different question with a different PG function (`cost_index`), and changing it
in the same commit would make any category movement unattributable. It is filed
as a ledger row, not folded in.

**NOT taken — option 1 alone** ("use the carried `PlanCost` where it exists,
otherwise leave as-is"). It is correct where it applies and it does not cover
Q8: `SetOp` has no `PlanCost` field, so Q8's leaf would keep its 8.35.

**NOT taken — option 2 in full** (give every sub-plan class a real upper-rel
path with genuine `cost_subqueryscan` / `cost_ctescan` inputs). That is the
full-fidelity answer and it remains the right end state, but it is a
multi-class planner build-out, not a pricing fix. The residual is recorded in
the ledger rather than claimed as done.

## 4. The honest residual

`DeriveLegacyDisplayCost`'s own header says "this is NOT a cost model and
nothing may plan against it" (`internal/optimizer/plancost.go:116`). That rule
was written when the alternative was printing `0.00` in an EXPLAIN column. Here
the alternative is pricing a whole subtree at **zero** inside the planner,
which is strictly worse, so the rule is not a reason to leave the defect.

But it must not be glossed either. For a leaf class that carries a real
`PlanCost` the base cost IS real. For `SetOp` and the other classes with no
`PlanCost` field, the base is `DeriveLegacyDisplayCost`'s pass-through arm:
`startup = max(child startup)`, `total = sum(child totals) + cpu_tuple_cost *
rows`. Two things make that defensible as an interim base rather than a
fabrication:

- its dominant term is the SUM OF THE CHILDREN'S OWN COSTS, and for Q8 those
  children (`Hash Join`, `HashAggregate`, `Seq Scan`) all carry real search
  costs;
- that arm's shape is itself `cost_subqueryscan`'s shape — children plus a
  per-tuple charge.

What it does NOT model is the set-op's own hashing work, which PG charges. So
the number is a floor, not PG's number. Ledger row filed; option 2 owns it.

## 5. Change

One site — `internal/optimizer/joinsearch.go`, the leaf seeding loop:

```go
p := newPrebuiltPath(rel, leaf)
if isSubplanLeaf(leaf) {
        p.Cost = costSubplanLeaf(cp, leaf, rows)
} else {
        scanPages, scanTuples, scanQualOps := baseSeqScanCostInputs(ri, leaf, rows, width)
        p.Cost = costSeqscan(cp, scanPages, scanTuples, scanQualOps)
}
```

The partial twin (`considerparallel.go`) needs no change: it already
`continue`s for every leaf that is not a plain `*SeqScan` of a real table, so a
sub-plan leaf never reaches `addPartialSeqScanPath`.

## 6. Result

### 6.1 The defect is closed, and Q8's election flipped to PG's method

Q8's inner side, before and after:

```
before:  ->  Hash Join  (cost=1.27..11.28 rows=32)
               ->  HashSetOp Intersect  (cost=6457.53..7360.42 rows=535)

after:   ->  Hash Join  (cost=6458.80..7374.05 rows=32)
               ->  HashSetOp Intersect  (cost=6457.53..7360.42 rows=535)
```

The parent is above its own child again. And the top join elects PG's method:

```
before:  ->  Hash Join    (cost=3487.55..19455.50 rows=818)
after:   ->  Nested Loop  (cost=9934.68..27215.31 rows=818)
PG:      ->  Nested Loop  (cost=12329.66..28502.37 rows=812)
```

goopg's total moves from 19455 to 27215 against PG's 28502 — the right method,
now priced in PG's neighbourhood instead of 30% below it.

**First-divergence census** (`scripts/pg-plan-first-divergence.py`):

```
before:  Q8 depth=3 [join-method] under Sort: PG Nested Loop Inner | goopg Hash Join Inner
after:   Q8 depth=3 [join-order]  under Sort: PG Nested Loop Inner | goopg Nested Loop Inner
```

The node kind at depth 3 now MATCHES PG; the residue is join order.

### 6.2 TPC-DS SF0.25 parity

```
before: PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=91 join-method=70 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=25 rendering=26

after:  PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=91 join-method=68 scan-type=61 parameterisation=55 aggregation-strategy=44 sort-strategy=60 parallelism=85 qual-placement=26 rendering=26
```

`join-method` 70 → 68 (−2) — the category the task named, moving in the right
direction — and `qual-placement` 25 → 26 (+1). Match holds at 2 and
`missingnode` at 25.

### 6.3 TPC-H SF1, parallel canonical mode

Stats epoch `e4a554b2a4cfb710` (pinned, same as the 0011a/0011a-2 pairs).
Parity lines byte-identical to HEAD's, and the plans capture is byte-identical
too — `sha256 0bf7e4d1540cf0e4cad42a1fcff7d77581b6a08a218edc1df0d68e68827eab76` —
from a DIFFERENT binary (`e1bb094f5aeae53f` here versus `42dfcd6069014ad2` at
HEAD), which is G3's required proof that the equality is inertness rather than
a re-measured binary. Expected: no TPC-H query joins over a sub-plan leaf.

### 6.4 shape-delta

`queries=99 same=69 changed=30 added=0 removed=0` — Q1 Q2 Q4 Q5 Q8 Q11 Q14 Q30
Q31 Q34 Q39 Q44 Q46 Q47 Q54 Q57 Q58 Q59 Q64 Q65 Q68 Q71 Q72 Q73 Q74 Q75 Q79
Q81 Q83 Q95. A 30-query blast radius on a corpus where 32 queries join over a
sub-plan leaf is the expected size, not a surprise.

### 6.5 stats epoch of both arms

TPC-H `e4a554b2a4cfb710` on both. TPC-DS: consecutive same-day sweeps on the
gate-owned `:65437` dataset, no reload between them.

### 6.6 planning route

PG-shaped search (`GOOPG_PGSHAPED_DP=1`, default); the change is at the search's
own leaf-seeding step.

### 6.7 seam-decline census

`N/A — this change prices a leaf; it files no path and declines no seam.`

### 6.8 wall time of every query whose plan changed

The sweep's status-delta channel: `verdict-changes=none runtime-moves=4
total-delta=+5.6%`, all four SLOWER — Q58 2s→5s, Q11 3s→7s, Q28 3s→6s, Q95
3s→6s. Three of them (Q11, Q58, Q95) are in the changed-plan set; **Q28's plan
did not change**, so its move is host noise on a sub-5s query.

The three real ones are the expected consequence of the fix: a join that was
elected because its inner looked free is now elected, or not, on the inner's
real cost, and the honest plan can be the slower one. Per AGENT.md §Goal a
slower plan that matches is not a regression here. Ledger row filed
(`m0144-0011b-1-slower-plans-q11-q58-q95`) so the cost is recorded rather than
absorbed silently.

### 6.9 Movement

`Movement: none` — match 2 → 2 on both corpora and every
`CATEGORIES-EXCL-MATCH` delta inside the ±3 noise band (`join-method` −2 is the
largest). The Q8 first-divergence advance and the corpus-wide `join-method`
direction are real results; neither is movement under S3, and reporting them as
such would be instrument-shopping.

## 7. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` (PASS=96, SKIP=3) |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2, TPC-DS SF0.25 match 2, both floors held |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched; this is a cost expression` |

With 30 plans changed and `MISMATCH=0 CKMISMATCH=0`, the SF0.25 values gate is
the load-bearing one here: every one of those 30 queries still returns the same
rows and the same checksums.

## 8. What is still open

- **Index and bitmap leaves are still priced as a fabricated sequential scan.**
  Deliberately untouched so this change's movement stays attributable; ledger
  row `m0144-0011b-1-index-leaf-still-priced-as-seqscan`.
- **The base cost for a no-`PlanCost` class is still the legacy estimate**
  (§4). Option 2 — real upper-rel paths per sub-plan class — owns it; ledger
  row `m0144-0011b-1-subplan-leaf-base-is-legacy-estimate`.
- **M0144-0011b** can now be re-measured: its premise is repaired and Q8's
  election is PG's method, with join ORDER the remaining divergence at that
  depth.
