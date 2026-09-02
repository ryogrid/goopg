# P2-A — the planner context

Implementation design for TODO **P2-01**. Parent design:
[08 §5.1](../08-target-design.md). Gate: [09 §5](../09-verification-and-acceptance.md) P2 row.

Revision 2 — corrected after agent review, which found **two blockers**: the
threading targeted the wrong constructor, and the proposed accessor would have
introduced a **cross-session plan corruption** by walking a package-global
parent pointer. Three of the four verification items in revision 1 were shown to
be vacuous. Each correction is marked **[R2]**.

---

## 1. What is wrong

`defaultCostParams()` (`internal/optimizer/cost_funcs.go:83`) returns hard-wired
constants and is the only source of cost parameters in the planner. Exactly
three production call sites (verified, no others):

| site | what it prices |
|---|---|
| `joinsearchseam.go:337` | the PG-shaped join search (`joinlistProblem.cp`) |
| `planner.go:9241` | `bitmapOverCorrelatedProbe` (declared `:9237`) |
| `plancost.go:117` | `DeriveLegacyDisplayCost` — display only, deleted by Phase 4 |

All nine cost GUCs are registered and settable and reach **nothing**.
`cost_funcs.go:75-79` records both the gap and its hazard:

> The two must agree or the planner prices a geometry the executor will not
> build. … The per-session value does not reach the planner yet: cost time has
> no session in scope.

That hazard blocks **P0-12**: setting `work_mem = 64MB` in the bench conf today
would make the executor honour 64MB while the planner prices at 512MB.

**[R2]** There are already **two** session→planner channels, and this adds a
third; the doc must say so rather than pretend it is the first:

1. Six **process-global** setters bridged at `cmd/goopg/main.go:398-431`
   (`enable_nestloop_index`, `enable_memoize`, `enable_presorted_aggregate`,
   `enable_hashagg`, `geqo`, `geqo_threshold`). A `SET` in one session changes
   the planner for every session. That is P2-02c, out of scope here.
2. `sessionPlanCatalog` (`dispatch.go:1799`) carries four scan toggles onto the
   planner through `catalog.Catalog` fields (`DisableSeqScan` … ,
   `dispatch.go:1816-1828`). Whether `PlannerSettings` should eventually absorb
   these is a Phase 6 consolidation question, explicitly **not** decided here.

## 2. Scope **[R2 — corrected]**

Revision 1's "In" list said "the cost GUCs reaching the two real cost sites",
which contradicts §4.1 and §5: **no GUC reaches anything in this commit.**

**In:** the carrier type; the `PlanWithSettings` entry point; the threading; and
the two real cost sites reading *from the carrier* instead of calling
`defaultCostParams()` directly. Every caller stays on the defaulting wrapper.

**Out:** filling the carrier from a session (P2-02), changing any default
(P2-02b), moving the six process-globals (P2-02c), plan-cache keying (P2-04),
`disabled_nodes` (P2-05).

This commit is **plan-neutral by construction**.

## 3. The carrier

`ParallelSettings` (`internal/optimizer/parallel.go:61`), built from the session
by `applyParallelPostPass` (`internal/postmaster/dispatch.go:1492`), is the
established precedent, and `sessionMaxParallelWorkersPerGather`
(`dispatch.go:1555`) is one of a real family that already includes
`sessionWorkMem` (`:1646`).

```go
type PlannerSettings struct {
    SeqPageCost, RandomPageCost                       float64
    CPUTupleCost, CPUIndexTupleCost, CPUOperatorCost  float64
    ParallelSetupCost, ParallelTupleCost              float64
    EffectiveCacheSize float64 // BLOCKS  — GUC is UnitKB, BootVal "4GB"
    WorkMem            int64   // BYTES   — GUC is UnitKB, BootVal "512MB"
}
func DefaultPlannerSettings() PlannerSettings
```

**[R2]** The unit mismatch is called out in the field comments because it is a
real trap for P2-02: both GUCs are registered `UnitKB`
(`internal/utils/misc/defaults.go:835`, `:785`) while the planner wants blocks
and bytes respectively.

`DefaultPlannerSettings()` is defined as the values `defaultCostParams()` reads
— one source, not a second copy of the constants.

**[R2]** Revision 1 justified keeping `PlannerSettings` and `costParams` as
distinct types by claiming "several tests construct `costParams` directly".
There are **zero** `costParams{}` literals outside `cost_funcs.go:84`. The
conclusion stands for a better reason: ~200 test sites call
`defaultCostParams()` and mutate fields (e.g.
`cost_sort_external_test.go:89-90`), so `costParams` must stay an unexported,
freely-mutable internal currency while `PlannerSettings` is the public boundary.

## 4. Threading **[R2 — rewritten; revision 1 targeted the wrong constructor]**

### 4.1 Entry

`Plan(stmt, cat)` has **30** non-test call sites. Its signature does not change:

```go
func Plan(stmt parser.Stmt, cat catalog.Catalog) (Node, error) {
    return PlanWithSettings(stmt, cat, DefaultPlannerSettings())
}
func PlanWithSettings(stmt parser.Stmt, cat catalog.Catalog, ps PlannerSettings) (Node, error)
```

### 4.2 Down to the cost sites

**[R2]** Revision 1 proposed adding a field to `resolveContext`, censusing the
23 `&resolveContext{` literals, and resolving it by walking `parent`. All three
parts were wrong:

- The real constructor is **`newResolveContext(bindings, schema)`**
  (`planner.go:494`), called at **30** non-test sites. The 23 literals are
  FROM-item / SRF / VALUES expression scopes (`planTableFuncRangeVar`,
  `planRowsFrom`, …); **none of them reaches a cost site**. `planSelect`'s own
  context comes from `newResolveContext`.
- `parent` is **not set at construction**. `planSelect` assigns it afterwards
  from a package-level global: `ctx.parent = planParent`
  (`planner.go:1094`; `var planParent *resolveContext` at `:13791`, whose own
  comment calls it goroutine-thread-unsafe, and which `plan.go:663` cites as a
  known cross-session leak).
- **Therefore a `parent`-walking accessor is unsafe.** Under concurrent
  planning it could walk into *another session's* context and return that
  session's GUCs — a silent wrong plan, not the graceful degradation revision 1
  claimed. **The root walk is dropped.**

**Revised mechanism.** `newResolveContext` takes the settings as a required
parameter:

```go
func newResolveContext(bindings []rangeBinding, schema Schema, ps PlannerSettings) *resolveContext
```

The compiler then enumerates every site that must supply them — no source-text
census, no silent default. `planStmt` (1 caller) and `planSelect` (**12**
callers — **[R2]**, revision 1 said 15, miscounting the declaration and three
comment mentions) take the settings as a parameter and pass them down.

### 4.3 The three cost sites

1. **Seam** (`joinsearchseam.go:337`): reads `ctx.settings`. **[R2]** No walk
   needed — `tryJoinSearch` is reached only from `predp.go:127`,
   `planner.go:1214` and `:1258`, all inside `planSelect`, all with
   `planSelect`'s own context.
2. **`bitmapOverCorrelatedProbe`**: its caller `planIndexScanFromWhereShape`
   (`planner.go:9392`) already has `ctx` in scope, so it takes the settings as a
   parameter. **[R2] But `planIndexScanFromWhere` is reached from three
   places** — `planner.go:1126` (`planSelect`, fine), `:11727` (`planUpdate`)
   and `:11899` (`planDelete`). The DML two build their context with
   `singleBindingContext` (`planner.go:531`), which is parentless and has no
   statement above it. So `SET random_page_cost` would not affect an
   `UPDATE`/`DELETE`'s index-vs-bitmap choice. **Decision: `planUpdate` and
   `planDelete` are threaded too**, in this commit — they are two call sites,
   and leaving DML on defaults would be an undocumented divergence of exactly
   the kind this project keeps finding.
3. **`DeriveLegacyDisplayCost`** keeps `defaultCostParams()`: display-only,
   deleted by Phase 4, and threading a session into the EXPLAIN renderer for
   numbers no decision reads is scope creep.

## 5. Filling it from the session — and a prerequisite this design must flag

A `sessionPlannerSettings(sess)` helper in `internal/postmaster`, beside the
existing family. **Not wired here**; that is P2-02.

**[R2] P2-04 is a prerequisite of P2-02, not a later item.**
`internal/postmaster/plancache.go:42` is a server-level cross-session cache
keyed on `(dbOid, normalizeCompatSQL(sql))` with **no GUC fingerprint**
(`dispatch.go:1780-1785` says so). Its only guard is `plannerScanTogglesActive`
(`dispatch.go:1786`), which checks four scan GUCs and none of the nine cost
GUCs. P2-02 converts `dispatch.go:1161`, which sits inside the cache-guarded
block — so P2-02 as currently written would let one session's
`random_page_cost` leak into another session's cached plan.

**This commit is unaffected** (every caller stays on defaults, so nothing
session-dependent enters the cache), but TODO must be reordered:
**P2-04 (or at minimum extending `plannerScanTogglesActive` to the cost GUCs)
before P2-02.**

## 6. Why not the alternatives

| alternative | why not |
|---|---|
| A package-level `SetCostParams()`, like the six `enable_*` bridges | That is the defect P2-02c exists to remove, and `planParent` (§4.2) shows where it leads. |
| Put the fields on `costParams` | ~200 test sites mutate `costParams` freely; it must stay internal. |
| `context.Context` | The planner takes none, and a typed field is checkable. |

## 7. Verification **[R2 — three of four items were vacuous]**

1. **A non-default `PlannerSettings` passed through `PlanWithSettings` lands in
   `joinlistProblem.cp`.** This is the only test with real content and it is
   load-bearing. It must cover a statement containing a **subquery**, not only a
   flat join, so a settings drop on a recursive `planSelect` branch is caught.
2. A second case for `bitmapOverCorrelatedProbe` (a correlated subquery), and
   one each for `UPDATE` and `DELETE` per §4.3.2.
3. `RALPH_PRECOMMIT_SCOPE=units`, `internal/{optimizer,executor,postmaster}`,
   and the existing SPOT / DS05 gates.

**Dropped:** revision 1's TPC-H 22 before/after capture. Plan-neutrality here is
*guaranteed by construction* — every caller keeps the defaults and
`plancost.go` is untouched — so a full S-cold sweep would cost hours to confirm
something a typo in one constant list is the only way to break, and which risk
R4 already forecloses. It also could not detect the risk it was aimed at,
because no non-default settings exist to be dropped.

**Dropped:** the `&resolveContext{` census test. It covers 23 sites none of
which reach a cost site, and misses all 30 that do. The compiler-enforced
parameter in §4.2 replaces it.

## 8. Risks

| risk | mitigation |
|---|---|
| **[R2]** A parent walk reads another session's settings via `planParent`. | Dissolved: no walk. Settings are a required constructor parameter. |
| Settings dropped on one recursive `planSelect` branch. | §7.1's subquery case; and the compiler flags any `newResolveContext` site that omits them. |
| `costParams` and `PlannerSettings` drift. | One conversion function; `DefaultPlannerSettings()` is defined as what `defaultCostParams()` reads. |
| **[R2]** P2-02 leaks a session's cost GUCs into the shared plan cache. | Not this commit, but recorded in §5 and reordered in TODO so it cannot be discovered later. |
| Unit confusion when P2-02 fills the struct (`UnitKB` vs blocks/bytes). | Field comments state the units; P2-02's tests must assert the conversion. |
