# Step (d) recon: the FK chain does NOT move plan parity. Measured, and it is a no-go.

Recon only — no scope, no code, no change to the shared corpus. Run on a
throwaway `cp -a` clone of `bench/tpch/runtime_goopg/data` at
`/tmp/r127clone` (port 5533), deleted afterwards, exactly as the R126
FK-loss measurement was run before its scope.

**Purpose:** R125 built step (a) and R126 step (b) to make step (d) —
declaring TPC-H's eight foreign keys — possible, on the thesis that FK
evidence would fix Q9's join order. Before scoping (d), measure whether
it can pay.

## 1. Declaring the eight FKs now works, instantly

All eight declared `NOT VALID` in one statement batch, sub-second, on the
real SF=1 corpus:

```
 conname               | child    | parent   | conkey
 customer_nation_fk    | customer | nation   | {3}
 lineitem_order_fk     | lineitem | orders   | {2}
 lineitem_part_supp_fk | lineitem | partsupp | {8,5}
 nation_region_fk      | nation   | region   | {3}
 orders_customer_fk    | orders   | customer | {3}
 partsupp_part_fk      | partsupp | part     | {1}
 partsupp_supplier_fk  | partsupp | supplier | {2}
 supplier_nation_fk    | supplier | nation   | {2}
```

That is R125 (NOT VALID is legal evidence, so O(1) instead of ~2 weeks of
validation) plus R126 (the rows persist, with the two-column `{8,5}`
array intact) working end to end on the real corpus. The chain's
*machinery* is sound.

## 2. It buys nothing. TPC-H parity is unchanged.

| | match | shapediff | missingnode |
|---|---|---|---|
| no FKs (R126 baseline, `r126-tpch.plans.txt`) | **6** | 14 | 2 |
| all 8 FKs declared | **6** | 14 | 2 |

Categories move slightly the **wrong** way: `join-method` 10→11,
`scan-type` 9→10, `qual-placement` 4→5. `join-order` stays 14 — the
category the whole chain was aimed at.

### Q9, the target, in detail

```
no FKs : Q9 SHAPE-DIFF [join-order]
         goopg cost=109733.79 rows=122      | pg cost=139669.14 rows=60125
8 FKs  : Q9 SHAPE-DIFF [join-order,join-method,scan-type,qual-placement]
         goopg cost=374413.11 rows=5000     | pg cost=139669.14 rows=60125
```

The FK arm **is live and is not redundant with the index arm** — the
estimate moves `122 → 5,000`, 41x closer to PG's 60,125 (still 12x low).
So the reviewer's hypothesis that `keysCovering`'s index arm would make
the FK redundant is **refuted**: `partsupp_pk` is unique on exactly the
join columns and the index arm does see it (measured: an instrumented
`keysCovering` reports `partsupp … indexes_default_dbOid=3`), yet the FK
still changes the estimate. The two arms are not interchangeable — the
index arm supplies an upper bound, which does not bind on an
*under*-estimate; the FK arm supplies the selectivity that raises it.

**But the better cardinality makes the plan worse.** Q9 goes from
diverging on one dimension to diverging on four, and its cost more than
triples. This is R125's reviewer's warning realised in the unfavourable
direction: correcting cardinality changes shape, and the new shape is
further from PG's.

## 3. Verdict

**The FK chain is a no-go as a plan-parity lever.** Step (d) should not
be scoped as a parity round. R125 and R126 both remain correct and
worthwhile on their own merits — R126 in particular fixed a silent
correctness bug (referential integrity lapsing across a restart) — but
the parity thesis that motivated them does not survive measurement.

Anyone tempted to revive it should first explain why a *more accurate*
Q9 cardinality produces a *less* PG-like plan. That question is the real
lead here, and it points at the cost model or the join-order search, not
at missing FK evidence.

## 4. What the A/B could NOT establish, and the bug that stopped it

I attempted a same-epoch A/B (declare → capture → **drop** → capture) so
the two arms would share one stats epoch. **The drop arm is void**:

```
ALTER TABLE nation DROP CONSTRAINT nation_region_fk;   -->  ALTER TABLE
SELECT count(*) FROM pg_constraint WHERE contype='f';  -->  8
```

`ALTER TABLE … DROP CONSTRAINT` on an FK **reports success and does
nothing** on this database. Root cause, read from source:
`InMemory.DropForeignKeyConstraint` hardcodes
`c.tableByOID(tableOID, DefaultDBOid)` (`catalog.go:22241`) and returns
`false` when that misses, while `execAlterTableDropConstraint`'s FK arm
discards the result and returns nil regardless
(`operators_ddl.go:13275`).

**Pre-existing, not introduced by R126** — that function is untouched by
this round — but R126 makes it matter more, because an FK that cannot be
dropped now also cannot be dropped by restarting. It is the same
hardcoded-`DefaultDBOid` family as the bug R126's own review caught in
`resolveFKCatalogKeys`, and `HasPrimaryKey` (`catalog.go:22261`) has the
same shape.

It is also **exactly the gap R126's reviewer named**: no in-process pin
crosses a database boundary, so the per-DB paths have manual evidence
only. This is the second per-DB defect found in two rounds.

Consequence for the numbers above: the FK-on capture is compared against
the R126 baseline taken on the **shared corpus in an earlier epoch**, not
against a same-epoch drop arm. The **match count is robust** (6 in both,
and unchanged across every capture in this workstream), but the
per-category deltas and the exact Q9 cost should be treated as
**indicative, not pinned**. Re-measure them after the DROP bug is fixed
if anyone needs them to be load-bearing.
