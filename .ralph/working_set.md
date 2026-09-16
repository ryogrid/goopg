Task: M0142-0008d — recon the EXPLAIN self-correlated-EXISTS alias bug
(census-found, filed as "small/self-contained"). DONE this loop as a
**recon, not a fix** — closed `[x]` with corrected sizing; the actual fix
is now M0142-0008e (open, filed this loop). No production code changed.

Files: `docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md` (new
§9, full root-cause writeup + repro). `docs/design/README.md` (index entry
extended with §9 summary). `.ralph/fix_plan.md` (M0142-0008d ticked `[x]`
with corrected findings; new `M0142-0008e` task filed with exact resume
points). `.ralph/deferral_ledger.md` (new row `M0142-0008d`).

Key symbols: `unnestExistsExpr` (`internal/optimizer/unnest.go:4181`) —
`outerKey`/`innerKey` construction at `:4379-4396` copies each side's
`ColumnRef.SourceTableIdx` verbatim from its PRE-FLATTEN per-level value.
`liftResidualConjuncts`'s `*ColumnRef` case (`unnest.go:4056`) — same bug
for lifted residuals (e.g. Q21/Q16's `<>` Join Filter). `explainNames`
(`internal/executor/explain_names.go:37`), specifically `bySrc
map[int16]int32` (`:82`) and `collect()` (`:157`) — holds ONE relation name
per raw `SourceTableIdx` value; `explainSingleSourceIdx` (`:355`) derives
that value from a scan node's OWN `Output()` schema, not from the
join-level ColumnRefs, which is why a one-site patch to
outerKey/innerKey/residuals does NOT fix the bug. `clonePlanReplacingOuter`
(`unnest.go:1492-1998`, 15 `Node` cases, ~500 lines) is the only existing
code that walks every `Node` kind `innerPlan` can contain post-strip — the
fix (`remapSourceTableIdx`) needs to be its sibling.

Findings this loop:
- **Reproduced independently of TPC-DS** (not just read the code): a
  throwaway single-table self-correlated EXISTS on a scratch `/tmp` cluster
  (port 5533, `scripts/goopg-test-run.sh`) shows the IDENTICAL bug —
  `Hash Cond: (cs1.cs_order_number = cs1.cs_order_number)` /
  `Join Filter: (cs1.cs_warehouse_sk <> cs1.cs_warehouse_sk)` instead of
  `cs1 = cs2`. So it's a general EXISTS-unnesting defect, not
  TPC-DS-schema-specific. TPC-DS Q16/Q94 showed the OPPOSITE side losing
  (both print `cs2`), confirming the collision direction is walk-order
  dependent, not fixed either way.
- **Root cause**: `ColumnRef.SourceTableIdx` restarts at 1 per query level
  (documented, intentional — `explain_names.go:70-81`). The outer table and
  the EXISTS body's table can coincidentally share a raw value (e.g. both
  are "first table in their own FROM list"). Once `unnestExistsExpr`
  splices the body into the outer tree as an ordinary `Join.Right` child
  (no longer reached via `NodeSubplans`), `collect()` walks both scans in
  ONE tree and they race for the same `bySrc` key; the loser's name is
  wrong wherever a `ColumnRef` carrying that raw value is qualified.
- **Blast radius is narrower than feared, in one direction**: `Join.schema`
  for a Semi/Anti join is `outerChild.Output()` only (`unnest.go:4470`) —
  nothing above the join ever sees an inner-scope column, so ONLY the
  join's own Predicate/LeftKey/RightKey can render wrong. Verified a
  non-lifted residual left on the scan (`cs2.cs_ship_date_sk > 3`) renders
  bare/unqualified, not wrong — PG's own `varprefix=false` scan-qual rule
  means the collision is dormant there. Node LABELS ("Seq Scan on ... cs2")
  are correct too — a separate disambiguation pass (`nodeLabels`).
- **But the fix is bigger than feared, in the other direction**: fixing
  only the join-level `ColumnRef`s' `SourceTableIdx` does NOT fix it,
  because `collect()` resolves relation names off the SCAN NODE's OWN
  schema, not off the join-level refs. A fully correct fix must renumber
  the ENTIRE `innerPlan` subtree consistently (every node type that stores
  its own `schema` field, not just the leaf scan) — sized against the
  `clonePlanReplacingOuter` precedent (~500 lines, 15 cases), a real
  K24-class increment, not a 3-line patch.
- Execution/row counts are provably unaffected in all cases tested
  (matches PG row-for-row) — this is a pure EXPLAIN-display defect.

Next step: implement **M0142-0008e** (fix_plan.md, filed this loop) —
`remapSourceTableIdx(node Node, offset int16) Node` in
`internal/optimizer/unnest.go`, mirroring `clonePlanReplacingOuter`'s case
set, applied to `innerPlan` right after it's built (`unnest.go:4277`) with
an offset guaranteed larger than any `SourceTableIdx` in
`outerChild.Output()`; apply the SAME offset to `innerKey`
(`unnest.go:~4390`) and the residual's inner-side `ColumnRef`s
(`liftResidualConjuncts`, `unnest.go:~4056`). Needs a unit test asserting
EXPLAIN text directly (row-count gates cannot catch this bug class).
Repro (no TPC-DS data needed): `CREATE TABLE t(a int, b int); EXPLAIN
SELECT 1 FROM t t1 WHERE EXISTS (SELECT 1 FROM t t2 WHERE t2.a = t1.a AND
t2.b <> t1.b);`. Not on the critical path for M0142-0008a-3(i)/(ii) or
M0142-0008c — pure display correctness, independently landable. If -0008e
is judged too large for one loop when picked up, decompose further before
implementing (same precedent as M0142-0008a/M0140-0006).

Gates run: no production code changed this loop (docs/fix_plan/ledger
only), so the practice-card code-change gates (tpch-spotcheck, TPC-DS
SF0.25 sweep, `go test`) do not apply. `make ralph-state-guard` — same
recurring benign status/progress marker mismatch as prior loops (previous
loop's clean-exit `progress.status=completed` vs current loop's
`status=running`), auto-repaired, then passed. Throwaway `/tmp` repro
cluster (port 5533) stopped and removed after use, per lifecycle rules.

In-flight: none.
