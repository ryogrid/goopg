(idle — nothing in flight)

Last loop (#8, 2026-07-31) landed **M0125-0035's binary-join arm** as `5054dac0`
(pushed). `internal/planner/inner_join_qual_pushdown.go` (+`innerJoinPushLeafScan`;
`innerJoinPushEligibleInput` now admits base-relation leaves; fail-closed
idempotence guard on the AND-in path), its test file (the old
`…DeclinesOnBaseRelationLeaf` pin INVERTED, + an idempotence pin),
`bench/tpch/env_goopg.sh` (`GOOPG_BIN` override), design
`docs/design/0125-0035-c2-single-table-qual-placement.md`, evidence
`analysis/m0125-0035-c2-qual-placement/`.

**-0035's mandated first step is DISCHARGED: C2 is an EXECUTION defect, not
costing-only.** Serial `EXPLAIN ANALYZE` — a COUNTING instrument, so valid on
the loaded host — shows `date_dim` hashed at **actual rows = 73,049** and the
join emitting **1,374,770** rows for a 275,107-row answer; the MHJ arm is the
same. `multiHashJoinOp.Open` drains every build child and hashes all of it, then
`partitionFilters` evaluates a build-table qual at STEP time.

Three findings the next loop should not rediscover:
1. **The D2 leaf scoping borrowed the wrong risk.** Slice A MOVES a conjunct
   pre-DP (so it changes join ORDER — that is what "Q8/Q21 PASS→CANCEL" meant);
   `pushSingleSideQualsIntoInnerJoinInputs` runs LAST and DUPLICATES, so order is
   already fixed. Proven on real plans: TPC-H plan-diff 4/22 with **zero**
   structural change, 4 net-new scan filters, **0 removed**.
2. **A planned subtree is re-walked once per enclosing scope.** Without the new
   guard Q69 printed its `d_year`/`d_moy` conjuncts TWICE. Any future pass in
   this tail of `planSelect` must be idempotent.
3. **`SmallDimension` is a hardcoded name-tag** (`region`/`nation`, initdb/open.go),
   so Slice A can never fire on TPC-DS. This change ROUTES AROUND it; the DP still
   sees unfiltered base-rel sizes (ledger row (c), → `M0125-0038`).

Gates: units PASS; `tpch-spotcheck.sh` `RESULT=PASS` (Q12=2 Q13=35); plan-diff vs
`m0125-0034-setop-join-promotion` 4/22 DIFFER, all additive; **full 99-query SF0.5
gate PASS=87 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=8 SKIP=4 — no new timeout**,
all 8 already filed. Sweep ran `FORCE=1` under the live nightly: legitimate for
row/checksum work, but **every wall clock in it is contaminated** — no timing was
taken this loop.

**-0035 STAYS OPEN — acceptance Q78 is untouched** (qual sits above two
`Hash Join (LEFT)` and names a CTE output column). Per the banner the next
selection is the **shared CTE/outer-join arm of `-0034` + `-0035`**: preserved-side
pushdown for outer joins (safe without a `nullingrels` model) + PG's
single-reference CTE inlining (PG 12+ `cte_inline`). Re-read the banner before
selecting; it outranks this note.

Host note: nightly CI batch was live all loop (load ≈ 8). Private binaries
`tmp/goopg-m0125-0035-bin` and `-spotcheck` used everywhere — the shared
`tmp/goopg-bench-bin` was NOT clobbered this time (that is what the
`env_goopg.sh` override fixes). All goopg clusters left DOWN as found; :65438
(PG) was already UP and is left UP.
