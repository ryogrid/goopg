# M0142-0003e — the superkey/FK no-fan-out mechanism is not the problem; the bench cluster's schema is

## Task

Filed by M0142-0003d's Finding 1/2: goopg's DP search estimates the bare
`lineitem ⋈ partsupp` composite join (`l_partkey = ps_partkey AND l_suppkey =
ps_suppkey`) at 2406 rows against a true value of ~5,999,098 (every `lineitem`
row has exactly one matching `partsupp` row). `partsupp_pk` is a genuine
2-column `UNIQUE` index on exactly `(ps_partkey, ps_suppkey)`. goopg already
has purpose-built code for exactly this pattern
(`internal/optimizer/joinkeyproof.go`'s `superkeyJoinEstimate` /
`internal/optimizer/joinrelsize.go`'s `superkeyJoinSelectivity`, M0127-P5.6-f),
but it evidently was not preventing the 2500x collapse. The task asked for a
targeted unit test to determine, without guessing, whether the mechanism
reaches this case and if not why.

## Method

1. Re-confirmed `GOOPG_PGSHAPED_DP`'s live default (per
   `goopg_arm_scripts_disable_dp_search` in memory — an arm script disabling
   the DP search would make a real fix look like a no-op). `grep` across
   `internal/optimizer/*.go` shows the flag defaults **ON**
   (`joinsearch.go:76`, `pgShapedDP = pgShapedDPFromEnv(os.Getenv(...))`,
   documented at ~40 call sites since M0127-P5.9, 2026-08-06). The
   FK/superkey arm is live in production, not a dead/disabled branch.
2. Rather than writing a new unit test from scratch, found that
   `internal/optimizer/joinrelsize_test.go` **already contains a test with
   the exact numbers of this repro** —
   `TestCalcJoinrelSizeCompositeUniqueRetainsEqualities` uses `lineitem`
   (6,000,000 rows) / `partsupp` (800,000 rows, `NDistinct`
   200000/10000 on `ps_partkey`/`ps_suppkey`), creates the identical
   `partsupp_pkey (ps_partkey, ps_suppkey)` `UNIQUE` index, and asserts the
   result is the **marginal independence-assumption product**
   `6,000,000 * 800,000 / 200,000 / 10,000 = 2400` — matching the 2406
   observed at HEAD to rounding. Its sibling
   `TestCalcJoinrelSizeBareCompositeDefaultsKeepEqualityAndBound` names the
   mechanism directly in its assertion: `superkeyJoinSelectivity` on a bare
   (non-FK) composite `UNIQUE` key returns `boundProven=true, fired=false` —
   i.e. the bound is recognised, but selectivity substitution deliberately
   does **not** fire. Ran both plus `TestCalcJoinrelSizeFKDividesByParentCount`
   and `TestEstimateJoinBareCompositeUniqueDefaultsKeepEqualityAndBound`
   (the `cardinality.go` production twin) — all four **PASS at HEAD**
   (`go test ./internal/optimizer/... -run '...'`).
3. Read `provableJoinKeys` (joinkeyproof.go:672-760): a bare `UNIQUE` index
   match is deliberately admitted only into the **bound-only** first loop of
   `superkeyJoinEstimate`/`superkeyJoinSelectivity` (`admit: func(k) bool {
   return !k.fromFK }`) — it can clamp `rowsBound` (an upper-bound clamp
   against the OTHER side's row estimate, `joinKeyRowsBound`) but never
   multiplies `est.sel`. Only a **declared foreign key** on the join column
   set is admitted into the second loop (`admit: k.fromFK`), which is the one
   that does `est.sel *= 1.0 / key.rawTuples` and sets `fired = true`. This
   matches upstream: PG's own no-fan-out substitution
   (`get_foreign_key_join_selectivity`, `selfuncs.c`) also requires a
   `pg_constraint` foreign-key row, not just a unique index on the referenced
   side — a bare unique index proves the *referenced* side has no
   duplicates, not that every row on the *referencing* side finds a match.
4. So the code path is correct and reaches this exact case; the open
   question was whether the **real** `lineitem`/`partsupp` relationship in
   the TPC-H schema is declared as an FK. Checked both live clusters
   read-only (`\d`, `pg_constraint`, `pg_indexes` — no writes, no restarts):

   - **goopg's HammerDB-loaded bench cluster (`:65433`, db `tpch`)**:
     `SELECT * FROM pg_constraint WHERE conrelid='lineitem'::regclass` returns
     **zero rows**. `lineitem_part_supp_fkidx` is a plain non-unique btree
     index on `(l_partkey, l_suppkey)` — its name suggests a foreign key but
     it is not a `pg_constraint` row. No table in this cluster has *any*
     declared FK.
   - **The PG 18.3 oracle (`:65432`, db `tpch`)**: `SELECT * FROM
     pg_constraint WHERE contype='f'` returns the **full canonical 8-row TPC-H
     FK set** (`lineitem_partsupp_fk`, `lineitem_order_fk`,
     `partsupp_part_fk`, `partsupp_supplier_fk`, `order_customer_fk`,
     `supplier_nation_fk`, `customer_nation_fk`, `nation_region_fk`) — the
     standard `dbgen`/`ri.ddl` reference constraint set.
   - PG's own `EXPLAIN` for the bare join (`lineitem, partsupp WHERE
     l_partkey=ps_partkey AND l_suppkey=ps_suppkey`) estimates **rows=5999098**
     — essentially exact — via `Nested Loop` + `Memoize` +
     `Index Scan using lineitem_part_supp_fkidx`, i.e. PG's planner reaches
     the *same* FK-based estimate goopg's `superkeyJoinSelectivity` would
     reach if the FK existed on goopg's side (`TestCalcJoinrelSizeFKDividesByParentCount`
     shows the identical mechanism producing `rows ≈ childRows` for an
     FK-child-to-unique-parent join at these exact table sizes).
   - `grep` across `bench/tpch/*.sh` and `bench/tpch/tcl/build_schema.tcl`
     (HammerDB's own schema builder) for `FOREIGN KEY`/`ADD CONSTRAINT`:
     **zero matches** — HammerDB's script never declares any FK on either
     side. Nothing in the repository issues the 8 `ALTER TABLE ... ADD
     CONSTRAINT ... FOREIGN KEY` statements that the PG oracle carries; they
     must have been added to the PG side by hand, outside any tracked
     script, at some point after the HammerDB load, and never mirrored onto
     goopg's cluster.
5. Confirmed goopg's FK machinery is not the blocker: `catalog.ForeignKey`
   (`internal/catalog/catalog.go:1655-1690`) is a fully-fledged struct
   (`RefTable`/`RefColumns`/`OnDelete`/`OnUpdate`/`Deferrable`/`NotValid`/
   `MatchFull`/`NotEnforced`), and the parser already has purpose-built
   support for the HammerDB TPC-H `FOREIGN KEY (cols) REFERENCES table
   (cols)` shape specifically (`internal/parser/ast.go:3389`'s comment names
   it explicitly) via both table-level `FOREIGN KEY` and
   `ALTER TABLE ADD [CONSTRAINT] FOREIGN KEY`. There is no missing engine
   capability here — only a missing post-load DDL step on goopg's bench
   cluster.

## Finding (decisive — settles M0142-0003e)

**The superkey/FK no-fan-out mechanism is implemented correctly and reaches
this exact case; it does not fire because the join truly is not backed by a
declared foreign key in goopg's bench cluster's schema — but it IS backed by
one in the PG oracle's schema.** This is a **data/schema parity gap between
the two clusters** introduced by the bench setup, not a planner or
cost-model defect:

- goopg's `:65433` TPC-H cluster (loaded via HammerDB's `build_schema.tcl` /
  `build_schema_goopg.sh`) has **no foreign key constraints at all** on any
  of its 8 tables.
- The `:65432` PG 18.3 oracle has the complete canonical TPC-H FK set,
  added by an untracked manual step, never mirrored to goopg's cluster.
- goopg's cost model, given the same FK metadata PG has, would produce
  essentially the same near-exact estimate PG does
  (`TestCalcJoinrelSizeFKDividesByParentCount` reproduces the mechanism at
  matching scale) — the estimator is not the thing that needs fixing.

This reframes the M0142-0003 series' causal story once more. -0003a/-0003b
established the tie-break/enumeration-order questions were not it; -0003c
measured a "real 64% PG-side cost gap" by forcing PG into goopg's own winning
shape — but that PG measurement carries PG's *own*, FK-informed, near-exact
row estimate at the `lineitem ⋈ partsupp` step throughout, while the shape
being costed on goopg's side carries the 2500x-collapsed estimate from the
missing FK. The two costs are not pricing intermediates of comparable size,
so -0003c's 64% gap cannot be interpreted as a pure cost-formula
discrepancy until this schema gap is closed and the comparison is redone.

## What this does NOT establish

- Whether closing the schema gap (adding the 8 FK constraints to goopg's
  bench cluster to match the PG oracle) changes Q9's *chosen plan shape*, or
  merely its estimated cost within the same shape. Not measured this loop —
  requires a live DDL change against the shared bench cluster plus a fresh
  `EXPLAIN`/estimate-audit re-capture, which is new work, not recon.
- Whether other TPC-H/TPC-DS queries currently attributed to a "row-estimate
  collapse" (M0142-0004/0004a/0004b and siblings) have the same root cause
  (a missing bench-cluster FK) rather than an estimator bug. Not audited this
  loop — worth a quick corpus-wide `pg_constraint` diff between clusters
  before trusting any other collapse finding's causal story.

## Resolution

M0142-0003e is answered: **no code change is indicated.** The mechanism
(`superkeyJoinEstimate`/`superkeyJoinSelectivity`/`provableJoinKeys`) is
correct and PG-faithful. The next actionable step is a bench-setup fix (add
the missing FK constraints to goopg's TPC-H cluster to restore parity with
the PG oracle), filed as **M0142-0003f** — a different KIND of task
(bench/data-load DDL, not planner code) from every prior task in this
series, sized and gated separately.

## Cross-references

- `.ralph/fix_plan.md` M0142-0003a/b/c/d/e/f
- `internal/optimizer/joinkeyproof.go` (`provableJoinKeys`,
  `superkeyJoinEstimate`)
- `internal/optimizer/joinrelsize.go` (`superkeyJoinSelectivity`)
- `internal/optimizer/joinrelsize_test.go` (`TestCalcJoinrelSizeCompositeUniqueRetainsEqualities`,
  `TestCalcJoinrelSizeBareCompositeDefaultsKeepEqualityAndBound`,
  `TestCalcJoinrelSizeFKDividesByParentCount`)
- `bench/tpch/tcl/build_schema.tcl`, `bench/tpch/build_schema_goopg.sh`,
  `bench/tpch/setup_pg.sh`
- PG oracle upstream behaviour: `postgres/src/backend/utils/adt/selfuncs.c`
  (`get_foreign_key_join_selectivity`)
