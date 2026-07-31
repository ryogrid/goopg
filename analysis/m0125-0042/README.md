# M0125-0042 — root cause: an OR-ed `IN (subquery)` operand keeps a stale column index

Date: 2026-07-31 (loop #11). Engine: HEAD `08f45255`, binary
`tmp/goopg-m0125-0042-bin`. Data: TPC-DS SF0.5, goopg `:65437` db `postgres`,
PG 18.3 oracle `:65438` db `tpcds05`.

**This loop diagnosed; it did not fix.** No engine file changed. All
instrumentation quoted below was reverted before commit.

## The defect in one line

The `InExpr` operand of an OR-ed `col IN (subquery)` sublink carries the RIGHT
column **Name** and the WRONG column **Index**; at runtime it reads a different
relation's column, and string↔int coercion turns that into a *silent wrong
answer* instead of an error.

## Measurements (all against the PG 18.3 oracle)

| probe | shape | goopg | PG | verdict |
|---|---|---|---|---|
| `probe35g.sql` (filed reproducer) | EXISTS + (A OR B) | **1329** | 1294 | WRONG |
| `p2.sql` | EXISTS + A only | 377 | 377 | ok |
| `pB.sql` | EXISTS + B only | 950 | 950 | ok |
| `pOR_noexists.sql` | (A OR B), no EXISTS | 11127 | 11127 | ok |
| `pE.sql` | EXISTS only | 11996 | 11996 | ok |
| `pAA.sql` | EXISTS + (A OR A) | **314** | 377 | WRONG |
| `pAempty.sql` | EXISTS + (A OR ∅) | **314** | 377 | WRONG |
| `pconstop.sql` | EXISTS + (const IN …) | 11996 | 11996 | ok |
| `psingleton.sql` | EXISTS + value set `{351}` | **0 rows** | 1 row (351) | WRONG |

The filed framing ("an over-match of 35") understates it. `pAA` returns 314 rows
of which only **10** are in PG's 377 — the two answer sets are nearly DISJOINT
and merely similar in size (`goopg-pAA-rows.txt` vs `pg-pAA-rows.txt`).

Three facts localise it precisely:

- **The SEMI join is correct** — `pE` matches, and all 314 emitted rows satisfy
  the EXISTS when re-checked on PG.
- **The value set is correct** — `pconstop` (a CONSTANT operand over the same
  two OR-ed sublinks) is exact, and only **10 of 314** emitted rows satisfy the
  IN predicate.
- **So the operand is what is wrong.** `pAA` (both arms identical) and
  `pAempty` (second arm empty) are both wrong, so this is one arm mis-evaluating,
  not an OR-combination defect.

## Root cause

`EXPLAIN (VERBOSE)` shows the `Hash Join (SEMI)` publishing the OID-re-sorted
40-column MHJ layout — `customer_address`(0..12) ++ `customer_demographics`(13..21)
++ `customer`(22..39). The correct index for `c.c_customer_sk` is therefore **22**.

Runtime instrumentation in `evalInExpr` (`trace-runtime-operand.txt`):

```
M42: operandIdx=9 rowWidth=40 operand={KindString "77838"} row=[{KindInt 9027} …]
```

Index **9** of that layout is **`ca_zip`** — a *string*. goopg is asking
`ca_zip IN (<integer ws_bill_customer_sk set>)`; `compareEq`'s string↔int
coercion makes the comparison *succeed* rather than raise, so a ZIP code that
numerically appears among the customer keys admits the row. That is the silent
wrong answer, and it is why no substitution of any single `customer` /
`customer_address` / `customer_demographics` column reproduced goopg's 314: the
compared value is a ZIP, not a key.

Planner instrumentation (`trace-planner-operand.txt`) traces the index back:

| stage | index | that index means |
|---|---|---|
| `planInExpr` (bind time) | **13** | `c_customer_sk` in a `ca ++ c` layout |
| entering `remapWithBindings` | **9** | `c_customer_sk` in a `cd ++ c` layout |
| after `remapByPosMap` | **9** | unchanged — the map is a no-op here |
| required at runtime | **22** | `c_customer_sk` in `ca ++ cd ++ c` |

So the operand is resolved against **partial two-table join layouts** as the
plan is built, and no later pass corrects it. `posMap(27) = 9` — i.e. the one
remap that could have moved it treats 9 as already-final and leaves it alone;
it is not the remap that is broken.

**Why nothing catches it:**

1. `applyJoinTreePosMap` never reaches this node's re-resolution. When
   `remapWithBindings` runs on the outer query, the tree is
   `Filter → MultiHashJoin` — there is no `*Join`, so `reresolveJoinByName`
   (the only pass that re-resolves a predicate's ColumnRefs **by Name**) is never
   called for the SEMI join. The trace shows `M42P` lines only for the
   subqueries' INNER joins.
2. **A single `IN` is masked**: it is unnested into a semi-join whose keys go
   through `rebind`/`predRebind`, which resolve **by Name** — and the Name is
   correct. Only the OR-ed form, which cannot unnest and survives as a SubPlan
   filter, uses the raw Index.
3. **EXPLAIN masks it too**: the filter prints `c.c_customer_sk` (from the
   Name) while the executor reads index 9. A plan reader cannot see this defect.

The hashed probe is NOT involved: `GOOPG_HASHED_SUBPLAN=off` reproduces both
wrong answers identically (314 / 1329), as does
`max_parallel_workers_per_gather = 0`. The result is deterministic across runs.

## Why no fix landed this loop

The house pattern for exactly this hazard already exists —
`resolveHostOperandIdx` (`internal/planner/exists_to_any.go`, M0125-0036), which
re-resolves an operand **by Name** against the host row schema precisely because
"an index recorded inside a sublink is not trustworthy after MHJ packing". The
proposed fix generalises it to hand-written `InExpr` operands. That is a planner
change and carries the full bar (units + `tpch-spotcheck.sh` + the SF0.5 gate),
which did not fit the remaining budget after the diagnosis. Resume point is in
`.ralph/fix_plan.md` under `M0125-0042`.

## Reproducing

```bash
GOOPG_BIN=$PWD/tmp/goopg-m0125-0042-bin bash bench/tpcds/server.sh start sf05
psql -h 127.0.0.1 -p 65437 -U ryo -d postgres -tA -f analysis/m0125-0042/pAA.sql   # 314, want 377
psql -h 127.0.0.1 -p 65438 -U ryo -d tpcds05  -tA -f analysis/m0125-0042/pAA.sql   # 377
```

`pAA.sql` is the cheapest reproducer (one arm, ~35 s); `probe35g.sql` in
`analysis/m0125-0036-exists-to-any/` is the originally filed one.

A minimal synthetic reproducer was attempted (6 tiny tables, OIDs ordered so the
MHJ re-sorts, SubPlan bodies made joins, projected column moved off index 0) and
reaches a **structurally identical plan** while still answering correctly — see
"minimal shape does not reproduce" in the ledger. The trigger needs the
partial-layout binding history, not just the final plan shape, so the SF0.5
probes above remain the reproducer.
