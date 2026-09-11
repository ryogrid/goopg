# R66 Slice-2 STEP 0 — live agg-chain dump: chase targets named (2026-09-11)

SCOPE §6 (reviewed): name the per-query chase target for families G
(Q7/Q9 Group+Sort keys) and T (Q13 key2 + outer Group Key), or decline
with reason. No planner/executor change in Step 0 — kept (the only
tracked-tree delta was a temp env-gated stderr dump, reverted; no
tracked source modifications. Untracked-only: this STEP0 file; a
foreign throwaway `internal/executor/zz_probe_r66_test.go` (peer's R66
probe, 19:25 — left untouched per the foreign-WIP rule, never staged).

## 1. Protocol outcome

- **(i) prefers-unit-fixture: NEGATIVE, recorded.** There is no knob
  separating fixtures from corpus on this axis: `GOOPG_PGSHAPED_DP`
  defaults ON (M0127-P5.9; `TestPgShapedDPDefaultsOn`,
  `joinsearch_test.go:319`, assertion `:323-324`), so the R66-scope
  probes E/I already engaged the search and still resolved
  GROUP-BY-alias to source. The alias GroupExprs need something the
  fixtures lack (multi-table searched subtrees with boundary Projects),
  not a flag flip.
- **(ii) live dump: EXECUTED.** Temp `GOOPG_R66DBG` print in the
  Aggregate EXPLAIN arm (Aggregate shape, GroupExprs, Aggs, 6-deep
  child chain with own-vs-child output names, Project Targets),
  binary `/tmp/pp2/r66/goopg-r66dbg`, inode-verified launch on
  `/tmp/pp2/clone-tpch-r65` :5533, `tpch-runner -explain -queries
  7|9|13`, server stopped after. Evidence: `/tmp/pp2/r66/server-dbg*.log`
  (298 + full-target lines), `dbg-explain-q{7,9,13}.txt`.

## 2. Measured chains (all node contents dumped, not read)

**Q7 outer** (ngroup=3, Sorted): GroupExprs = `ColumnRef(alias, idx
0..2, table 1)`. Chain: Agg(4/4) → Sort(4/4, preserving) →
**Project(4/48, narrowing)** with Targets `[ColumnRef(n_name, idx41,
table 5), ColumnRef(n_name, idx45, table 6), ExtractExpr, BinaryOp]`
→ Project(48/48, all-ColumnRef) → NLI (stop).

**Q9** (ngroup=2, Hashed): GroupExprs = `ColumnRef(nation/o_year, idx
0..1, table 1)`. Chain: Agg(3/3) → **Project(3/50, narrowing)** with
Targets `[ColumnRef(n_name, idx47, table 6), ExtractExpr, BinaryOp]`
→ Project(50/41, mixed ColumnRef/NullConst — right-join null-extension
shape, immaterial: the chase stops at the first Project) → Join (stop).

**Q13-outer** (ngroup=1): GroupExprs[0] = `ColumnRef(c_count, idx1,
table 1)`; Aggs[0] = `count(*)` (already fixed Slice 1). Chain:
Agg(2/2) → **Project(2/2, preserving**, Targets `[ColumnRef(c_custkey,
idx0, table 0), ColumnRef(count, idx1, table 0)]`, i.e. pure
rename) → inner Aggregate (ngroup=1, output `[c_custkey, count]`) →
Sort(2/2) → Project(2/17, narrowing, Targets `[ColumnRef(c_custkey,
idx0, table 1), ColumnRef(o_orderkey, idx9, table 2)]`) → …

**Q13-inner**: Aggs[0] = `count(o_orderkey)` plain synthesisable, arg
`ColumnRef(o_orderkey, idx1, table 2)` (orders scope — renders
`orders.o_orderkey` through the existing qualifier).

## 3. Derived chase rule (what the arms round implements)

`chase(expr, node)` for a group-section key, entered after Slice-1's
Arm-S name guard:

- Sort/Filter with `nout == ncout` (checked at runtime): recurse
  `(expr, child)` — positions preserved.
- Project at chase position `j = expr.Index` (`0 <= j < len(Targets)`
  else decline), `t = Targets[j]`:
  - `t` bare ColumnRef with `SourceTableIdx == 0`, `t.Index` inside the
    child's output, child-output name match (Q13 `count`→`count`):
    **descend** `(t, child)` — pure rename, not source text.
  - `t` bare ColumnRef with a real table (Q7 `n_name` t5/t6, Q9 `n_name`
    t6; child-output name match required — chain outputs `[41]=n_name`,
    `[47]=n_name` verified in the dump): **stop, render `t`**.
  - `t` non-ColumnRef (ExtractExpr, …): **stop, render `t`**.
- Aggregate at chase position `j`: `j < len(GroupExprs)` → recurse
  `(GroupExprs[j], child)` (further group hop); Aggs-section →
  synthesise via the R65/R66 admission (reuse `expandAggOutputRef`
  path); else decline.
- Anything else (Join/NLI/scans): decline. Depth cap (~4), fail-closed.

Traces: Q7-key0 `supp_nation` → Sort → Targets[0] `n_name(t5)` stop →
`n1.n_name`; Q7-key2 `l_year` → Targets[2] ExtractExpr stop → Sort-arm
wrap → `(EXTRACT(year FROM lineitem.l_shipdate))`; Q9-key0/1 analogous
(`nation.n_name`, bare-Group vs wrapped-Sort per existing S18 rules);
Q13-key2 `c_count(idx1)` → preserving Project (rename only) → inner
pos 1 → Aggs[0] → `count(orders.o_orderkey)` (bare in Group Key,
wrapped in Sort Key — PG's exact split).

## 4. Predictions for the arms round (falsifiable)

Rendering set {Q7,Q9,Q10,Q13} → {Q10} (Q10 FD-trim stays planner
work); pp match unmoved (no Slice-2 query can close — conjunction
rule); ZERO new categories; ZERO EXTRA flips; values 24/24; DS sweep
PASS=96 SKIP=3. The Group Key arm moves for the first time (Slice 1
left it byte-identical) — corpus A/B with per-query adjudication is
the binding gate, plus a DS Star/Distinct-HAVING-style pre-flight
(review N4 carried: pre-flight, don't post-hoc).

## 5. Open soundness item (the arms SCOPE must close)

`SourceTableIdx == 0` as the derived-marker rests on three dumps +
fixture probes (sort-key refs table 0, pass-through Targets table 0,
base refs table N) — no code citation yet for what stamps table 0.
Two further uncited premises the rule uses: Sort
position-preservation (supported only by dump equality — `nout` names
== `cout` names — plus the runtime `nout == ncout` check; no planner
citation) and the Sort/**Filter** recurse step (**zero** Filter nodes
in any dump — the Filter hop rests solely on
`childAggregateThroughFilters`' comment). The arms SCOPE must
cite-or-drop all three (stamping site, Sort output-identity site,
Filter hop) or replace the tests with structural ones. Do NOT land any
of them on dump-count alone (K4 class).

Evidence tmp-only `/tmp/pp2/r66/server-dbg*.log`
(`GOOPG_R66DBG` prints reverted — `goopg-r66dbg` binary retained for
re-derivation, never shipped).
