# 0125-0035a — A restriction reaches the preserved side, and it reaches the bottom

**Task:** `M0125-0035` arm (a), taken together with `M0125-0034`'s open
CTE-reference arm as the fix_plan banner directs ("take the shared
CTE/outer-join arm of `-0034`+`-0035` together").
**Status:** implemented 2026-07-31 (loop #9). `M0125-0035` and `M0125-0034`
both STAY OPEN — see §6 for exactly what each still owes.
**Predecessors:** `0125-0004-*` (the pass), `0125-0035-c2-single-table-qual-placement.md`
(the binary-join arm, loop #8), `0125-0034-setop-join-promotion.md` (C1's
set-operation arm, loop #7).

## 1. What was wrong

`pushSingleSideQualsIntoInnerJoinInputs` is goopg's analogue of PG's
`distribute_restrictinfo_to_rels`
(`postgres/src/backend/optimizer/plan/initsplan.c`): a conjunct of a residual
`Filter` that references exactly one of the join's inputs is *duplicated* onto
that input, so a scan emits fewer rows instead of the join filtering them
afterwards. Loop #8 established by measurement that this is an **execution**
defect, not a costing one — the build side really is hashed at full table size.

Two independent restrictions kept the pass off the plans that need it most:

1. **INNER joins only.** Property 4 declined every outer join. Its own pin
   conceded the case it tested ("the qual here sits on the PRESERVED side of a
   LEFT JOIN, where pushing would in fact be safe") and justified the decline by
   the absence of a `nullingrels` model.
2. **The join's immediate input only.** The target had to be a direct child of
   the join carrying the residual `Filter`. PG has no such rule: a
   single-relation restriction is filed on that relation's `baserestrictinfo`
   however deep the join tree above it is.

Together they are why the two ACUTE members named in `M0125-0035`'s task body
were untouched by loop #8:

- **Q31** — six `CTE Scan` nodes on `ss`/`ws`; PG attaches the per-reference
  filter to each, goopg attached exactly one and hoisted the other five into a
  single conjunction on the top join. The five it missed were not the join's
  immediate children.
- **Q78** — `ss_sold_year = 1998` sits above two stacked `Hash Join (LEFT)`,
  on the preserved spine. Restriction 1 declined at the first one.

## 2. Why the preserved side is safe without a nullingrels model

For `A LEFT JOIN B` and a restriction `p` that mentions only `A`:

Every row of `A` reaches the join output at least once — matched, or
null-extended. `p` cannot read any of `B`'s columns, so its truth value on an
output row is fixed by that row's `A` half alone. Discarding an `A` row before
the join therefore discards exactly the output rows the `Filter` above would
have discarded, and discards nothing else. No reasoning about which rows were
null-extended is required, which is precisely what a `nullingrels` model is
for.

The nullable side is the case that genuinely needs one, and it still declines.
PG gets there from the other direction — a *strict* qual on the nullable side
lets `reduce_outer_joins` turn the join into an inner join first — and goopg
has no strictness model either, so it does not attempt it.

`FULL` declines (neither input is preserved). `SEMI`/`ANTI` decline for a
second, independent reason: their `Output()` is `Left`'s layout alone
(`Join.Output`, M0125-0008), so the merged `leftWidth`/`totalWidth` arithmetic
does not describe the space the `Filter`'s `ColumnRef`s live in.

`CROSS` is *admitted*: it is an inner join whose predicate is absent or was
demoted to a residual `Filter` — exactly `M0125-0034`'s C1 shape — and a
single-side restriction is worth more there than anywhere else, because it
shrinks an input of a Cartesian product.

All of this lives in one function, `joinRestrictionSides`, so the policy has a
single site rather than a condition repeated per call.

## 3. Descent

`pushConjunctIntoSubtree` replaces the old one-level attach. It carries a
conjunct down the join spine, re-basing it with `shiftConjunctForInput` at each
level, and attaches at the deepest node that can hold it. Three properties keep
it honest:

- **Coordinate correctness is structural.** A `Filter` does not change its
  child's `Output()`, and every `Join` level shifts by that level's own
  `leftWidth` before recursing, so the conjunct is always expressed in the
  coordinate space of the node currently being examined. The positional
  name validation of property 1 then re-checks that at every level, not just
  the first — a descent that drifts is rejected, not silently applied.
- **It does not stack.** Descent multiplies the number of places a re-walk
  could duplicate a conjunct (a planned subtree is re-walked once per enclosing
  scope — the Q69 double-print of loop #8). The `*Filter` case therefore
  descends past a `Filter` only when its child is a `Join`; when the child is a
  terminal target it ANDs into that `Filter` under the fail-closed `exprEqual`
  guard instead of wrapping the leaf a second time.
- **Coordinate conventions are never mixed.** A `Filter`'s `LeafLocal` flag
  describes its whole predicate, so a conjunct may only join one whose
  convention already matches what this pass would set. A mismatch declines.

Declined by construction, each for a stated reason: `MultiHashJoin` (its
coordinate space is the concatenation of `Tables`, and it has its own sibling
pass, `pushSingleSourceFiltersIntoMHJTables`), `NestedLoopIndexJoin` (its
`Outer`/`Inner` may be swapped relative to output order by
`pickInnerScanForNLI`'s flip), and any `LATERAL` join (owned by
`pushOuterQualsIntoLaterals`).

## 4. Measured

Evidence: `analysis/m0125-0035a-preserved-side-descent/`; plan re-capture
`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0035a/`.

**Plans (18-query timeout capture, SF0.5).** Two queries change, and they are
the two the task body named as acute:

- **Q31** — all six `CTE Scan` nodes now carry their own filter
  (`(d_qoy = N) AND (d_year = 1999)`), 5 net-new; that is PG's placement.
- **Q78** — `ss_sold_year = 1998` now reaches `CTE Scan on ss`, one level below
  where the pass used to stop.

The eight `Nested Loop (CROSS)` nodes of `-0034`'s open arm are **unchanged**.
Admitting `CROSS` lets a single-side restriction into a Cartesian input, but
none of the eight had one to give; their problem is that the *equi-predicate*
spans both inputs, which this pass by construction never moves.

**Answers (full 99-query SF0.5 gate, four chunks on one binary, quiet host):**

```
PASS=89 (54 ck-verified)  MISMATCH=0  CKMISMATCH=0  ERROR=0  TIMEOUT=6  SKIP=4
```

Against loop #8's run, **all 87 common PASSes are identical in both status and
value checksum**. Exactly two cells move, both `TIMEOUT → PASS`:

- **Q31 → PASS 11 s, 19 rows, `ck=2a74acfb556c21a7`** = the oracle. This is
  this change: it is one of the two plans that moved, and it moved from >300 s
  to 11 s.
- **Q18 → PASS 266 s.** Do **not** attribute this one here. Q18's plan is
  byte-identical across the change, and its prior reading was taken under the
  live nightly (loop #8 ran `FORCE=1`; every wall clock in that report is
  contaminated). 266 s against a 300 s cap on a now-quiet host is the more
  economical explanation. It stays filed as `M0125-0033`.

Timeout class **8 → 6** (Q30 Q35 Q64 Q65 Q78 Q81), of which at most one is
attributable to this change.

**TPC-H.** Plan-diff vs `m0125-0035-c2-qual-placement`: **1/22 DIFFER**, Q17,
with **zero structural change** — one added scan-level `Filter:`, no node-kind
line added or removed. Its effect on the estimate is the point: the inner join
falls `5,997,241,000 → 149,931` and its parent `8.99e13 → 2.25e9`, because
`p_brand`/`p_container` now reach the `part` scan.

Timed w2 arm (per-query-isolated harness, quiet host, base = loop #8's binary
`tmp/goopg-m0125-0035-bin`): **row counts identical on all 21 completing
queries**; stream 395.5 s → 389.1 s. Q17 — the one query whose plan changed —
is the largest single move at 0.84×, in the expected direction. Read both as
**neutral, not as a win**: `M0125-0031` measured this harness's single-run
per-query noise band at ~±17 %, so neither 0.84× nor −1.6 % clears it. Q21
times out in both arms, as it does in all four arms of `-0031` (`M0125-0032`).

`scripts/tpch-spotcheck.sh`: `RESULT=PASS` (Q12=2, Q13=35).

## 5. Tests

`internal/planner/inner_join_qual_pushdown_test.go`:

- `TestInnerJoinQualPushDeclinesOnUnpreservedJoin` — the old
  `…DeclinesOnOuterJoin` pin, narrowed to what still declines: FULL, SEMI,
  ANTI, and the *nullable* side of LEFT/RIGHT.
- `TestInnerJoinQualPushReachesPreservedOuterSide` — the inversion, both
  LEFT and RIGHT, including the index shift and property 2.
- `TestInnerJoinQualPushDescendsJoinSpine` — Q78's `Filter → Join(LEFT) →
  Join(LEFT) → CTE Scan` shape; also re-runs the pass to pin idempotence,
  which descent makes easier to break.
- `TestInnerJoinQualPushReachesCrossJoinInput` — CROSS participates, and the
  side-spanning conjunct beside it does not move.

## 6. What is still owed

`M0125-0035` — acceptance is **Q78 completing at `78|OK|45|8f67acff…`**, and it
still times out. Its qual now reaches `CTE Scan on ss`, but that filters the
CTE's output *after* the aggregate; the three channel CTEs still aggregate
every year. Reaching `date_dim` needs two mechanisms goopg does not have:
**single-reference CTE inlining** (PG 12+ `cte_inline`, `subselect.c`) so the
restriction can enter the CTE body at all, and then **equivalence-class
constant propagation** so `ss_sold_year = 1998`, tied to `ws_sold_year` and
`cs_sold_year` by the join conditions, reaches all three. Q31's `ws3` is the
multiply-referenced control that says inlining must be conditional on the
reference count. Arms (b) — the MHJ `InExpr` disqualification — and (c) — the
costing half — are untouched. Ledger rows 2026-07-31.

`M0125-0034` — its CTE-reference / derived-aggregate arm is **not moved**: the
same 8 crosses in Q30/Q64/Q65/Q81, all four still TIMEOUT. This loop's capture
narrows where to look next. In Q64 the two crosses are `date_dim d2` and
`date_dim d3`, whose equi-predicates
(`customer.c_first_sales_date_sk = d2.d_date_sk` and the `d3` twin) are demoted
to a `Filter` **two levels above** them — `customer` has not yet been joined
when `d2`/`d3` enter. That is a join-**order** defect, not a qual-placement
one, and it is a different mechanism from the set-operation arm loop #7 fixed:
the enumeration is placing `d2`/`d3` before the relation their predicate needs.
Start there, not in `pushOneConjunct`. Ledger row 2026-07-31.
