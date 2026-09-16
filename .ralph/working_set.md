Task: M0142-0008a-3i-plumbing-b2 — item 6's own scoping pass (design doc
§22.3/§23.3 deferred obligation). Recon only this loop, no production diff
(matches the established "prove/scope before coding" pattern of
-3i-plumbing-a/-b/-b1). NOT complete — b2 itself stays open.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §24 added.
- docs/design/README.md: m0142-0008a-1 index row extended with §24 summary.
- .ralph/fix_plan.md: `-3i-plumbing-b2`'s item 6 split into 6a/6b (see
  Findings). Task line itself stays `[ ]` — nothing landed.
- .ralph/deferral_ledger.md: `M0142-0008a-3i-plumbing-b2` row appended.

Key symbols: `unnestSubqueriesInPlan`/`walkSubqueryPlansInExpr`/
`unnestScalarWithResiduals`/`unnestSubquery`/`unnestInExpr`/
`unnestNonCorrelatedInExpr`/`unnestExistsExpr` (all internal/optimizer/
unnest.go — the 7 signatures item 6a would extend with a `*resolveContext`
param), `planSelectWithSettings` (planner.go:1533/:1619, the two call
sites that already hold `ctx`/`cat`), `rangeBinding`/`ctx.bindings`
(planner.go:549, the shared slice item 6b's synthetic leaf would live in
or avoid), `joinsearchseam.go:329` (the offset-agreement check the
synthetic leaf must satisfy), the `FOR UPDATE` `emit` closure
(planner.go:2523, the confirmed crash site if a nil-table binding reaches
it).

Findings (read before picking up -3i-plumbing-b2 again):
1. Finding A (§24.1) — the `*resolveContext` call-graph is SMALL and
   SETTLED: exactly 7 function signatures, all confined to unnest.go,
   rooted at 2 call sites in planner.go that already have `ctx`/`cat` in
   scope (`find_referencing_symbols`-confirmed, zero cross-package/test
   callers of `unnestSubqueriesInPlan`). This is item 6a: mechanical,
   ready to code, no design decision needed.
2. Finding B (§24.2) — the REAL size of item 6 is what happens once a
   synthetic `rangeBinding` for the Semi/Anti RHS leaf exists: `ctx.bindings`
   is read at ~24 other sites across planner.go, none audited. Spot-checked
   2: unqualified column lookup (`planner.go:7821`) is safe (`qualifiedOnly`
   check gates it before `b.table` is touched); `FOR UPDATE`/`FOR SHARE`
   with no target list (`planner.go:2523-2546`) is NOT safe — it
   unconditionally dereferences `b.table.OID` with no `qualifiedOnly` (or
   any) guard, so a query like `... WHERE EXISTS (...) FOR UPDATE` would
   nil-pointer-panic the moment this path goes live. `qualifiedOnly` alone
   does not make a synthetic binding safe.
3. Two structural options for item 6b, neither designed in detail:
   (1) add a stronger `opaque` flag and audit/gate all ~24 sites — correct
   but large, and every missed site is a live crash risk (the FOR UPDATE
   site proves the risk is real, not hypothetical); (2) keep the leaf's
   offset/width bookkeeping in a side-channel `joinsearchseam.go` reads
   directly, never exposing it via `ctx.bindings` at all — matches the
   leaf's own "opaque, non-reorderable" semantics (§22.2/§23.1) better and
   removes the audit burden by construction, but its exact shape (how
   `joinsearchseam.go:329`/`:339` would read it instead of
   `ctx.bindings[i]`) is undesigned.

Next step: design item 6b concretely (recommend starting from option 2,
the side-channel — write out its exact shape: what `joinsearchseam.go:329`
and `:339` would read instead of `ctx.bindings[i].offset`, and how
`unnestExistsExpr` populates it) BEFORE coding item 6a's plumbing — coding
6a first would just be unused wiring with nothing yet deciding what shape
the append takes. Once 6b is settled, code 6a (mechanical, low risk per
Finding A), then predp.go's pass-through, then the cutover
(`admitSemiAnti=true`), per fix_plan.md's -3i-plumbing-b2 entry (now
carries this loop's 6a/6b split).
Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 lands — both
still blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel
(fix_plan.md lines ~3891, ~3899).

Alternatives if -3i-plumbing-b2 is judged not worth continuing immediately:
M0141-S2b-2 (base join/scan Pathlist-across-search-boundary surgery — still
needs its own scoping pass). M0142-0003i/0003k remain BLOCKED on a
human-authorized shared `:65433` cluster reload — do not attempt.
Unrelated, NOT to be picked up under this banner unless explicitly
re-prioritized: `internal/parser`'s `GroupedJoinUnaliased` AST-drift gap
(pre-existing, traced to `dc91bd6b7`, ~60 failing test functions,
reconfirmed unchanged across 5+ consecutive loops now).

Gates run this loop: `go build ./...` clean (no production code touched,
this was a docs/plan-only loop). `make ralph-state-guard`: found and
auto-repaired the same benign prior-loop clean-exit marker as last loop;
consistent after repair.

In-flight: none. Nothing outstanding from this loop — ready to commit
docs/fix_plan/ledger changes.
