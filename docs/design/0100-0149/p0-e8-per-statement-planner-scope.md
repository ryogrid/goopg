# P0-E8 — concurrent sessions fail to resolve a CTE name: the planner's scope moves per statement

Status: done 2026-10-19. Owner-placed at banner item 0. Evidence: `analysis/p0e8/`, and the owner's report
`analysis/m0146/owner-ops-20261019/cte-first-statement-race.md`.

## 1. Symptom

Eight concurrent `pgbench -c 1 -t 50` clients ran `with x as (select 1 as a) select a from x;`. Some clients aborted
on their first transaction (0/50 processed) with `ERROR: relation "x" does not exist`, positioned at `x` in FROM. It
happened after a server start; later rounds on the same server never failed.

## 2. Cause

The planner kept per-statement dynamic scope in package-level variables. Each was set on entry to a nested scope and
restored on exit:

- `planCTEs`: the WITH list in scope (with.go).
- `planParent`: the outer resolveContext a nested sub-SELECT inherits (planner.go).
- The analyzer's `outerScope`: the lexical parent for a nested Analyze.

Their comments assumed "planning is single-goroutine per statement". The postmaster runs one goroutine per connection
and plans without a lock, so concurrent sessions swapped each other's scope. Session B's restore of `planCTEs` (to
nil) could land while session A was between registering its WITH list and resolving `FROM x`. A's lookup then fell
through to the catalog and raised 42P01.

Only a cold cross-session plan cache plans concurrently; later identical statements hit the cache. That is why only
the first statements after a start failed. The same inventory found two plain counters written during planning:

- `legHookFireCount`: a data race.
- `sublinkRouteCounts`: an unsynchronised map write, which can abort the whole process with Go's fatal
  "concurrent map writes".

## 3. PostgreSQL

PostgreSQL keeps this scope per query: `ParseState.p_ctenamespace` and `parentParseState` (parse\_cte.c,
parse\_node.h), and the planner's `PlannerInfo`. Each backend is a process with its own globals, so no session can
see another's scope.

## 4. Change

- **planState** (planstate.go) holds the statement's WITH list, outer resolveContext, and the analyzer's outer-scope
  slot.
- **stmtCatalog**, a wrapper over the catalog every planning function already receives, carries it.
  - `withPlanState` wraps the catalog once per top-level statement, idempotently, in Plan, PlanWithSettings,
    PlanSchemaOnly and ResolveColumnDefault. It also wraps in the internal functions a caller can enter directly:
    planStmtWithSettings, planSelectWithSettings, planFromClause and planSelectWithParent.
  - A re-entrant plan (a view body, EXPLAIN's inner statement, COPY (query)) passes the wrapped catalog and shares
    its statement's state, exactly as it shared the globals.
  - `Unwrap()` keeps the wrapper-peeling helpers (inMemoryCat, scanToggleDisabled) working.
- **Readers and writers** use plannedCTEsOf(cat), planParentOf(cat) and mustPlanState(cat). Six helpers gained a `cat`
  parameter: lookupPlannedCTE, markSelectOwnedCTEs, stampCTEReferenceCounts, cteAsDerivedItem,
  cteBodyNamesResolveAsDeclared, and derivedLeafColumns. So did the three FROM table-function planners and
  planJoinPredicate.
- **The analyzer** defines `OuterScopeCarrier`, which stmtCatalog implements. `SetOuterScope(cat, parent)` and the
  readers go through the carrier's slot. A top-level Analyze from DDL has no carrier, and so no outer scope, as
  before.
- **Counters:** `cteDeclSeq` and `legHookFireCount` are atomic; `sublinkRouteCounts` writes take a mutex.

Rejected: a global planning mutex. It fixed the race, but it serialises planning across sessions. On pgbench -S
(32 clients, 16 cores) throughput fell from 58.7k to 16.2k tps. The per-statement state measures 59.0k / 59.6k against
HEAD's 58.8k / 59.4k.

Left as is: `viewPlanDepth` (planner.go) is an atomic, process-wide view-nesting guard. Concurrent sessions add to the
same count, so a deep-enough concurrent load could raise a false "circular definition" past 64 levels. Ledger row
2026-10-19.

## 5. Verification

- TestConcurrentPlansKeepTheirOwnCTEScope (planstate\_test.go): 8 goroutines × 200 plans, mixing WITH, plain,
  correlated EXISTS and correlated scalar sublinks.
  - It passes under -race.
  - Mutation-checked twice. With no synchronisation, or with one state shared by all statements, it fails with the
    production error, `42P01: relation "x0" does not exist`.
- Server repro, 6 restarts × 2 rounds of the owner's pgbench script: HEAD 6 aborts in the first rounds, the fix 0.
- Gates:
  - units, TestPort\_RegressSuite, tpch-spotcheck and the acceptance arm (values identical) PASS.
  - The sf025 sweep is PASS=99.
  - tpcds-fireset PASSes with no fires: plans are byte-identical to HEAD on both corpora.
