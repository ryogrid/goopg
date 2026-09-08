# EX3-03 Cut 1, Step 2 — Thread session `work_mem` into planner cost time

Status: design (no code changed). Cut 1 = plumbing only: the planner prices the
same geometry the executor builds. No executor change in this cut.

## 0. The disagreement (why this cut exists)

At `work_mem = 64MB` the two sides of the sibling-path rule solve
`hashsize.Choose` for budgets 8x apart:

| side | expression | value at 64MB |
|---|---|---|
| planner (`internal/optimizer/cost_funcs.go:115`) | `hashsize.HashMemLimit(hashsize.DefaultMemLimitBytes, hashsize.DefaultHashMemMultiplier)` | 512MB x 2.0 = **~1 GiB** |
| executor (`internal/executor/operators_join_agg.go:716-740`, multiplier `ctxHashMemMultiplier` at :5108-5124, default 2.0) | `hashsize.HashMemLimit(ctx.WorkMem, ctxHashMemMultiplier(ctx))` | 64MB x 2.0 = **128MB** |

Witness (T მიმდინარე, `analysis/planner-refactor-take3/p401-retake-20260904/README.md`):
TPC-H SF1 Q9, S-cold serial, work_mem 64MB — goopg HEAD batches **2** (14.7 s)
vs PG batches 1 (6.2 s). The Slice-2 gate stays in MODEL currency
(`NBatch` 2->1); runtime `Batches: 2` is the BEFORE.

The gap is openly ledgered: `cost_funcs.go:66-80` ("cost time has no session
in scope ... Deferral ledger 2026-08-05 M0127-P5.7-a") and
`plannersettings.go:5-26` (P2-02 blocked on P2-04). P2-01/P2-02/P2-04 have
since landed, so the bridge the comment asks for now exists — this step uses
it. What remains is enumerated below: the search core is threaded, the
recursion fringes are not.

## 1. Current plumbing (file:line evidence)

**Carrier (exists).** `PlannerSettings` (`internal/optimizer/plannersettings.go:28-110`)
is the per-statement planner context, PG's `PlannerGlobal` analogue. `WorkMem`
is raw bytes (:44-46), `HashMemMultiplier` defaults on zero (:105-109).
`costParams()` (:160-189) applies the multiplier once via the SAME helper the
executor calls: `workMem: hashsize.HashMemLimit(ps.WorkMem, ps.HashMemMultiplier)`
(:173). `DefaultPlannerSettings()` (:118-152) round-trips the hard-wired
`defaultCostParams()` — raw `WorkMem: hashsize.DefaultMemLimitBytes` (:149),
NOT `cp.workMem`, so the multiplier is never squared (invariant pinned by
`TestDefaultPlannerSettingsMatchTheHardWiredParams`, `plannersettings_test.go:96-99`).

**Session fill (exists).** `sessionPlannerSettings` /
`ctxPlannerSettings` / `plannerSettingsFrom`
(`internal/postmaster/dispatch.go:1846-1952`): `work_mem` KB->bytes (:1939-1943),
`effective_cache_size` KB->blocks (:1945-1950), `hash_mem_multiplier` float
(:1888). Malformed values degrade to defaults, never to zero cost (:1836-1838).
Both channels exist because both are real — extended-protocol sites have
`sess`, the simple-query site only `ctx.GetSetting` (:1856-1864).

**Postmaster handoff (exists).** `PlanWithSettings` is called with session
settings at every wire site: `dispatch_extended.go:123`,
`extended.go:716,730`, `dispatch.go:834,880,1161,3645,4659` (last two are the
EXPLAIN paths via `ctxPlannerSettings`).

**Cache guard (exists, prerequisite already landed).** `plannerCostGUCsOverridden`
(`dispatch.go:1979-2004`) lists all ten cost GUCs incl. `work_mem` and
`hash_mem_multiplier`; `plannerSessionInputsActive` (:2011-2013) is the single
predicate all four guard sites call. A session with `SET work_mem` neither
reads nor writes the shared cache — pinned by
`plan_cache_cost_gucs_test.go:35-149` (override detection incl. SET-to-same-value,
unit round-trip, honours-overrides).

**Search core (exists, threaded).** `planSelectWithSettings`
(`internal/optimizer/planner.go:811`) stamps `ctx.settings`; `joinsearchseam.go:350-361`
feeds `cp: ctx.settings.costParams()` into `joinlistProblem` (`relfromjoinlist.go:100`),
which `buildInitialRels` (`relfromjoinlist.go:384`) and the GEQO branch
(`relfromjoinlist.go:419-426`, incl. effort/pool/generations) consume.
Subquery propagation is pinned by `settings_propagation_test.go:25-65`
(Q9's whole join tree sits inside a `from (select ...)` — the test that caught
the 245.7s -> 314.4s re-defaulting regression). Production rescan pricing
takes `cp`: `nestLoopInnerRescanCost(inner, cp)` (`joinpathsmemoize.go:447`,
callers `pathgen.go:137`, `joinpathsnli.go:254`).

**Precedent.** `ParallelSettings` (`parallel.go:56-82`) — plain value struct,
explicit parameter, zero value safe. `PlannerSettings` follows it deliberately
(`plannersettings.go:21-25`), except the enable_* zero value is NOT safe
(false = disabled), so every hand-built instance must start from
`DefaultPlannerSettings()` (`plannersettings.go:59-62`).

## 2. What still drops the session (the step-2 work list)

Every site below plans or prices under the hard-wired ~1 GiB while the
statement's executor builds 128MB at `work_mem = 64MB`:

1. **Set-op recursion** — `planner.go:929` (leftmost branch), `:957`
   (`planSegment` right operands), `:1061` (`SetOpOperand` grouping node).
   Each calls bare `planSelect`, discarding the `plannerSet` in scope.
   Fix: `planSelectWithSettings(seg.stmt, cat, plannerSet)` / thread through
   `wrapSetOpSortLimit` callers. Note the SetOp AST save/restore dance
   (:930-939, :958) is plan-cache-driven; keep it intact.
2. **CTE bodies** — `with.go:205` (`preplanWithClause`), `:264` (non-recursive
   under RECURSIVE), `:334` (anchor), `:395` (recursive member), plus
   `copy.go:41`. CTE bodies routinely contain the join tree (Q9-pattern), so these are the highest-value conversions.
   Each needs the statement's `PlannerSettings` as a new parameter; the
   "skip Analyze" rationale in the comments is orthogonal and stays.
2b. **Subquery interiors — the largest unlisted family (F1, REQUIRED).**
   `planSelectWithParent` (`planner.go:13824`) ends in bare
   `planSelect(stmt, cat)` at `:13844`, discarding settings. Every
   subquery shape funnels through it: derived tables
   (`planSubqueryRangeVar`, `:4098`/`:4112`), scalar subqueries
   (`planSubqueryExpr`, `:13597`), array subqueries
   (`planArraySubqueryExpr`, `:13611`). The Q9 witness itself — join tree
   inside `FROM (select …)` — plans its inner join search through exactly
   this path, so the cut as previously scoped would leave the witness
   unmoved. (Correction: `:13844` is the SUBQUERY path, not a "DML-SELECT
   site" as earlier drafted.) Same established pattern: thread
   `PlannerSettings` through `planSelectWithParent` + the subquery sites.
   The existing `settings_propagation_test.go:25-65` pin asserts the
   outer FROM context carries `WorkMem`, NOT that the inner SELECT's
   search does — extend it.
2c. **DML entry points (F2 — thread or explicitly scope out).**
   `planStmtWithSettings` (`planner.go:186`/`:198`/`:203`) calls
   `planInsert`/`planUpdate`/`planDelete` WITHOUT `plannerSet`, so DML
   statements never receive session settings at all; plus
   `INSERT…SELECT`'s bare `planSelect` at `:10949`. `UPDATE…FROM` /
   `DELETE…USING` join trees price at the default. Either thread settings
   through the three DML entry points or scope DML out with rationale —
   neither (current state) is not an option.
3. **`planSelect` wrapper itself** — `planner.go:807-809` (`planSelect` =
   `planSelectWithSettings(..., DefaultPlannerSettings())`). Keep as the
   fallback for the ~30 no-session callers (`Plan`, `planStmt`,
   `PlanSchemaOnly`, plpgsql/FK/DDL); the P2-A §4.3 enumeration rule stays:
   every remaining caller is a path with no session, not an oversight.
4. **`DeriveLegacyDisplayCost`** — `plancost.go:117` (`cp := defaultCostParams()`).
   NO CHANGE: scope-stated display-only, nothing plans against it. Documented
   here so the cut's `defaultCostParams` audit does not "fix" it.
5. **`pathRescanTotal`** — `joinpathsmemoize.go:371-375` (wraps
   `pathRescanCost(p, defaultCostParams())`). Currently test-only (sole callers
   are `joinpathsmemoize_test.go:229-230`); production uses the threaded
   `nestLoopInnerRescanCost`. NO signature change in this cut — but it is a
   latent 1GiB trap: if anyone wires it into production it reintroduces the
   disagreement silently. Either thread `cp` or delete at cut time; do not
   leave it as-is without a comment.
6. **`DefaultPlannerSettings()` spray in resolve contexts** — `planner.go`
   (:81, :601, :9991, :11070, :11152, :11212, :11257, :11730, :11760, :11799,
   :11910, :11936, :11966, :12025, :12047, :12054, :12132), `view_dml.go:251`.
   Each must be audited for "can a join search run below here?" — a resolve
   context that only resolves DML/DDL expressions is harmless; one above a
   sub-SELECT is another re-defaulting hole of the class
   `settings_propagation_test.go` pins. No bulk conversion: check each.
   PLUS zero-value `resolveContext` literals (F5):
   `&resolveContext{cat: cat, parent: planParent}` (`planner.go:4923`, and
   :4989/:5253/:5307/:5420/:5484) carry NEITHER Default NOR session
   settings. Today they only feed `resolveExpr` for SRF args
   (behaviour-neutral), but the audit rule as stated ("every remaining
   caller is a path with no session") doesn't cover them — a future cost
   reader of such a context prices at *zero* costs, not defaults. Include
   them in the audit or state a constructor rule (recommend: construct via
   a helper that stamps `DefaultPlannerSettings()` unless a session is in
   scope).

**Struct choice: none new.** Thread the existing `PlannerSettings` (by value,
as today) down the recursion in (1)-(2); derive `costParams()` once per search
as `joinsearchseam.go:361` already does. Do NOT thread `costParams` itself —
it is unexported and freely mutated by ~200 test sites
(`plannersettings.go:156-159`).

**Fallback when no session:** `DefaultPlannerSettings()` — definitionally
identical to today's behaviour (`planner.go:805-806`), so every unconverted
path is behaviour-neutral by construction. `sessionPlannerSettings(nil)` and
`ctxPlannerSettings(nil/no-GetSetting)` return it (`dispatch.go:1847-1848,
1866-1868`); EXPLAIN without a session plans at the ~1 GiB default, exactly as
today — a documented limitation, not a regression.

## 3. Hazards

- **H1 — `defaultCostParams` production audit.** After the cut, production
  callers must be exactly: `DefaultPlannerSettings` (definitional,
  `plannersettings.go:119`), `DeriveLegacyDisplayCost` (display-only,
  `plancost.go:117`), `pathRescanTotal` (test-only, `joinpathsmemoize.go:372`
  — thread or delete, §2.5). Any other production caller is a missed fringe
  site. (~200 test callers are untouched.) (F7: enforce the "exactly three"
  rule with a grep-gate in the cut's test, and resolve the §2.5 thread-or-
  delete item at cut time rather than leaving it conditional.)
- **H2 — tests pinning the 1GiB value.** `TestCostParamsWorkMemMatchesExecutorFallback`
  (`cost_funcs_test.go:190-199`), `TestDefaultPlannerSettingsMatchTheHardWiredParams`
  (`plannersettings_test.go:96-99`), `TestSessionPlannerSettingsRoundTripsUnits`
  (`plan_cache_cost_gucs_test.go:108-125`) all pin default==default. They must
  KEEP passing unchanged through the plumbing cut (behaviour-neutral proof);
  only the flip cut may touch them, and then only by retargeting the oracle.
- **H3 — GEQO path.** Already on `prob.cp` (`relfromjoinlist.go:419-426`,
  `geqo.go:338` carries `s.cp`). Hazard is asymmetric: a workload that crosses
  `geqo_threshold` only under session settings changes search algorithm AND
  budget at once. Gate must include a >=threshold shape or explicitly scope
  GEQO out.
- **H4 — cached-plan / prepared-statement paths.** Guarded (`plannerSessionInputsActive`),
  but the four guard sites (`dispatch_extended.go:51,127`,
  `extended.go:707`, `dispatch.go:1155`) must be re-verified to all call the
  COMBINED predicate — a site calling only the scan-toggle half would publish
  a 64MB-priced plan to every session. `TestPlannerSessionInputsActiveCoversBothFamilies`
  (`plan_cache_cost_gucs_test.go:78-96`) is the pin; extend it if a third
  family appears. (F4: name the DESCRIBE path — `describeViaPlanner`
  (`extended.go:707`) is one of the four, hot under pgbench `-S`
  Describe-per-Execute — and the PREPARE/EXECUTE split: PREPARE-time
  inference (`dispatch.go:834`) prices at PREPARE-time settings while
  EXECUTE (`:880`) re-plans at EXECUTE-time settings, so `SET work_mem`
  between PREPARE and EXECUTE is correctly re-priced; descriptor inference
  is width-only, harmless.)
- **H5 — EXPLAIN without session.** Falls back to defaults by design (§2).
  `EXPLAIN <q>` under a `SET work_mem` session goes through `ctxPlannerSettings`
  (`dispatch.go:3645,4659`) and matches; EXPLAIN from a sessionless path
  (background/utility) prices ~1 GiB while the executor, given a live ctx,
  builds session-sized. Accept and document; do not "fix" by threading a fake
  session.
- **H6 — multiplier double-application.** `PlannerSettings.WorkMem` is RAW
  bytes; `costParams()` applies `HashMemLimit` once. Any new fill site that
  copies `cp.workMem` back into `ps.WorkMem` squares the multiplier
  (the trap `plannersettings.go:144-149` records). New code takes `ps.WorkMem`
  only from the GUC (KB->bytes, `dispatch.go:1939-1943`) or from
  `DefaultPlannerSettings()`.
- **H7 — sort/spill symmetry.** `cp.workMem` feeds BOTH `hashJoinCost`
  (`cost_funcs.go:372`) and `costSortRun` (`cost_funcs.go:226-232`) plus the
  memoize/bitmap rescan spill arms (`joinpathsmemoize.go:403,476`,
  `bitmapMaxEntries(s.cp.workMem)` in `pathbitmap_test`-covered production
  code). Threading work_mem moves hash AND sort pricing together — correct
  (one budget, one currency, ch.04 §1) but it widens the plan-diff surface
  beyond hash joins. The gate's `changed=0` pin must cover sort-heavy shapes,
  not just Q9. (F6: `bitmapMaxEntries(cp.workMem)` moves with `work_mem`
  independently of hash/sort, so bitmap path selection can flip on its own —
  name bitmap-heavy shapes in the pin alongside sort-heavy ones.)
- **H8 — unit confusion at fill sites.** GUC reads back plain-KB integers
  (`dispatch.go:1840-1845`); bytes vs blocks conversions differ. Any new
  session->settings site must reuse `plannerSettingsFrom`, not hand-roll the
  conversion.
- **H9 — parallel cost interaction (F3).** `parallel_setup_cost` /
  `parallel_tuple_cost` are session-threaded via `costParams`
  (`cost_funcs.go:499-500`, `costGather` reads `cp`), and the cache holds
  the serial plan while `applyParallelPostPass` re-applies Gather
  per-session (`dispatch.go:1183-1186`). A `work_mem`-moved shape composed
  with per-session Gather insertion is untested territory. The gate's
  `max_parallel_workers_per_gather=0` serial setting neutralizes it — stated
  here, not assumed. A parallel-enabled flip cut needs its own
  parallel-shape pin.

## 4. Cut order

1. **Plumbing behind the same value.** Convert §2.1-§2.2 (+2b subquery
   interiors REQUIRED — the Q9 witness plans through `planSelectWithParent`,
   +2c DML entry points or explicit scope-out, + audit §2.6 incl. F5
   zero-value literals) to `planSelectWithSettings`, threading the
   statement's `PlannerSettings`.
   No default changes: with no session override every path still plans under
   `DefaultPlannerSettings() == defaultCostParams()`, so plans are bit-identical.
   Proof: full unit suite + plan pins green with `changed=0`; the 512MB arm of
   the gate (§5) shows zero movement.
2. **Flip the default (separate cut).** Only after (1) is green: make the
   session value actually differ (bench conf `work_mem`, or re-point the
   hard-wired default), and expect EXACTLY the §5 witness movement — anything
   else is a missed fringe site, not noise.

## 5. Falsifiable gate (Q9 witness)

Setup: TPC-H SF1, S-cold serial (`max_parallel_workers_per_gather=0`),
GOGC/GOMEMLIMIT 100/12GiB, port 65433 `tpch@tpch` (per the p401-retake record).

- **Witness arm (`SET work_mem = 64MB`):** planner prices the witness build at
  the 128MB budget — priced `NBatch` moves 1->2 on the build the executor
  already spills (`Batches: 2` at runtime, unchanged: executor is NOT touched
  in this cut). Model and runtime agree on "spills" for the first time.
- **Control arm (default 512MB session):** zero movement — same plans, same
  `NBatch`, same runtime within noise. Any movement here falsifies the
  "plumbing is behaviour-neutral" claim and blocks the flip.
- **Tripwires:** growth tripwire green (no server-side memory growth vs
  BEFORE); plan pins `changed=0` on the plumbing cut — or, if the flip cut
  moves plans, an explicit plan-bundle handoff listing every changed shape
  with PG-side acceptability, not a silent diff.
- **Pre-declared failure:** witness-arm priced `NBatch` stays 1 while runtime
  stays `Batches: 2` ⇒ the session value is still not reaching cost time
  (fringe site missed — re-audit §2); control-arm movement ⇒ the plumbing
  itself changed pricing (cut-1 violation — revert, do not "adjust" the gate).
