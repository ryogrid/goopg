# 0125-0024 — two expression-*identity* fail-opens that COLLIDE

Status: implemented 2026-07-30 (branch `tpcds-fix2`).
Task: `.ralph/fix_plan.md` → `M0125-0024`.
Discovered by: `M0125-0002`'s STEP 0 census
(`internal/planner/exprwalk_inventory_test.go`), ledger row 2026-07-30.
Depends on: `M0125-0001`'s child-slot primitive
(`docs/design/0125-0001-exprwalk-driver-and-exhaustiveness-gate.md`).

## 1. Why this is not "another partial walker"

The RC-1a class that `M0125-0001`/`-0002` exist to end is *fail-open
traversal*: a type switch over `Expr` meets a type it was never taught, does
nothing, and the caller loses an optimisation or (Q76) reads a stale index.
The two sites here are worse in kind, and the difference is what justifies
fixing them outside the seven-walker conversion scope:

> Both functions compute an **identity**. An unenumerated type is not
> *skipped* — it is **conflated with every other expression of the same Go
> type**.

A traversal that fails open loses information. An identity function that fails
open *invents* information: it asserts two unrelated expressions are the same
one. Downstream, that assertion is consumed as a licence to share state or to
treat one expression as already computed.

The precedent is `M0097-0032`, which is the same shape at the level above:
`aggregateCallKey` dropped `FILTER (WHERE …)` from an aggregate's dedup key, so
`count(*) FILTER (WHERE error IS NOT NULL)` collapsed onto `count(*)` and the
filtered count **reported the unfiltered total**. That was a shipped wrong
answer, found by reading a system view, not by any gate.

## 2. The two sites, as they stood at `da6d2c0c`

### (a) `planExprContentKey` (`internal/planner/planner.go:7023`, 4 of 32 arms)

```go
default:
        return fmt.Sprintf("%T", e)
```

Consumer: `buildAggregate`'s shared-state assignment (`planner.go:6060-6100`,
`M0097-0035`). Two user-defined aggregate calls that agree on
sfunc/stype/initcond/distinct/`argKey`/`filterKey` are given the same
`SharedStateSlot`, and the executor then calls `sfunc` **once** per row for the
whole group: `operators_join_agg.go:1699-1760` designates a leader and copies
the leader's finished state to every follower.

So the `default:` arm means: *any two distinct expressions of one unenumerated
type share a slot.* Two different `*CaseExpr` arguments both key to
`"*planner.CaseExpr"`; the second aggregate reports the first's value. The same
holds for `*CastExpr`, `*IsNullExpr`, `*BinaryOp` (!), `*SubqueryExpr` — 28 of
the 32 types.

Note `*BinaryOp` in that list: `ua(a + b)` and `ua(a - b)` collided. This is
not a corner reachable only by exotic SQL.

### (b) `exprEqual` (`internal/planner/planner.go:11946`, 5 of 32 arms)

```go
// Fallback: compare text representation (pointer-safe only for primitives).
return fmt.Sprintf("%T%v", a, a) == fmt.Sprintf("%T%v", b, b)
```

Consumers: the `DISTINCT ON` / `ORDER BY` positional-agreement check
(`planner.go:1623`, raises `42P10`), `distinctSortKeyOutputIndex`
(`planner.go:12044`), and `pathKeyEqual` (`pathkeys.go:28`).

`%v` on a pointer-to-struct prints `&{field …}` at the top level but prints any
**nested** pointer as a hex address, so for every type holding an `Expr` child
the fallback compares *addresses*: two structurally identical expressions
resolved from two textually identical fragments read **unequal**. It also
includes `pos` in the printed struct, so even a childless literal compares
unequal when it appears at a different source offset — which is the opposite of
`equal()` in PG, where `Const.location` is explicitly excluded
(`postgres/src/backend/nodes/equalfuncs.c` — `COMPARE_LOCATION_FIELD` is a
no-op macro).

Both directions are wrong, in opposite ways, and (b)'s wrongness is *lax on
some types and strict on others* depending only on whether a struct happens to
hold a pointer.

### The divergence nobody had noticed

(a)'s `*ColumnRef` arm keys `SourceTableIdx/Index`; (b)'s compares `Index`
alone. Two refs from different source tables at the same index are therefore
**equal to one and distinct to the other** — the sibling-divergence class of
`instincts` "sibling code paths must stay in sync", in a pair nobody had
identified as a pair.

## 3. What replaces them

One primitive and one driver, both in `exprwalk.go`, so the identity question
is answered in exactly one place:

| symbol | role |
|---|---|
| `exprSelfKey(e Expr) (string, bool)` | the node's **own** identity-bearing fields, excluding children. Complete over all 32 types; gated in both directions like `exprChildSlots`. |
| `exprIdentityKey(e Expr, pol scopePolicy) (string, bool)` | fourth driver over `exprChildSlots`: `exprSelfKey` + a parenthesised, delimited key per child slot. |

`planExprContentKey` and `exprEqual` become thin policy wrappers with **no type
switch of their own**, so both census pins are deleted (not demoted — unlike
commit 1's `remapByPosMap`, no per-type dispatch survives here).

### 3.1 The scope policy is `scopeVeto`, and that is the whole safety argument

An inner plan cannot be keyed: a `Node` tree has no identity function, and
`M0125-0001` deliberately left Node traversal out of `exprwalk.go` (ledger row
`tpcds-round2 exprwalk-node-side`). Under `scopeVeto` the driver **aborts**
when it meets `slotInnerPlan`/`slotSubqRow`, returning `ok == false`, i.e. *"I
cannot decide this structurally."* Both callers then apply their own
fail-closed direction — and the two directions are **opposite**, which is why
the shared layer returns a decidability flag rather than a bool:

| caller | `ok == false` must mean | why |
|---|---|---|
| `planExprContentKey` | **never share** → `fmt.Sprintf("opaque:%T:%p", e, e)` | sharing a state slot wrongly is a wrong answer. Keying by pointer preserves the one legitimate case (the *same* node reached twice). |
| `exprEqual` | **not equal** → `false` | claiming equality wrongly makes the planner treat one expression as another. A false negative at `planner.go:1623` is a spurious `42P10`, which is a diagnosable error, not a wrong answer. |

`scopeIgnore` would be a wrong-answer bug here (two different subqueries with
identical `Args` would key equal), so the policy argument is stated at the
driver and pinned by a test that asserts `scopeVeto` refuses what `scopeIgnore`
would accept. The parameter is kept rather than hardcoded because the policy is
the *caller's* correctness argument, and a driver that silently chose for the
caller is the shape this whole milestone is removing.

Both wrappers also short-circuit on **pointer identity** (`a == b`) before
keying. Today's `%T%v` fallback happens to return `true` for the same pointer;
without the short-circuit, `ok == false` would have made a node unequal to
itself.

### 3.2 What `exprSelfKey` excludes, and why each exclusion is safe

Two expressions with equal keys must be interchangeable *for evaluation*.
Everything excluded below is either derived from what is included, or is not
part of the value.

- **`pos` (every type)** — source location. PG excludes it from `equal()`; the
  whole purpose of a content key is to see through it.
- **cache fields** (`TypedStringLit.CacheValid/CachedTime`,
  `IntervalLit.Cache*`) — memoised from `Value`/`Unit`, which are included. One
  side parsed and the other not is the *same* expression.
- **resolved type metadata** (`ColumnRef.Type`, `ExecParamRef.Type`,
  `RowExpr.Types`, `FuncCall.ReturnType`, `ExtractExpr.SourceTypeName`) —
  derived from the included fields by the resolver. Including them would add
  only false negatives when one side was built by a path that leaves them
  empty.
- **`ColumnRef.Name` / `OuterColumnRef.Name`** — the struct comment says
  "resolved column name (for diagnostics)"; `Index` is authoritative and `Name`
  is empty on some construction paths.
- **`IsNonCorrelated`, `ParParam`** — subplan-lowering bookkeeping. Both live
  only on the four plan-bearing types, which are undecidable anyway whenever
  their `Plan` is set; when `Plan == nil` these describe the lowering, not the
  value. The `Args` children *are* keyed.
- **`ColumnRef.SourceTableIdx` / `OuterColumnRef.SourceTableIdx`** — the
  divergence of §2. Resolved **in favour of `Index` alone**, i.e. (b)'s
  behaviour, not (a)'s. Reasons, in order of weight:
  1. `SchemaColumn.SourceTableIdx`'s own doc (`plan.go:27-37`) says **zero
     means "unknown / derived"** and is assigned only for base-table
     FROM-bindings. It is auxiliary disambiguation metadata with a hole, not a
     `varno`.
  2. Both call sites compare expressions resolved against the **same**
     coordinate space, where `Index` *is* the identity: two refs with the same
     `Index` in one scope are the same column, whatever their metadata says.
  3. Consequently including it can only produce false negatives (one column
     read as two) — harmless for (a), a spurious `42P10` for (b).

  This makes (a) marginally laxer on `*ColumnRef` and leaves (b) unchanged.
  Anywhere the *source table* genuinely matters the planner already has
  `findColumnIndexByNameAndSource`/`predRebind`; identity is not that question.

- **`FuncCall.Name` case** — folded to lower case (as (a) already did, and (b)
  did not). Unquoted identifiers are case-insensitive in PG, so this is a fix,
  not a relaxation.

## 4. Behaviour deltas, by direction

Stricter (former collisions, now distinct) — the wrong-answer fixes:

- (a) any two distinct expressions of one unenumerated type: 28 of 32 types,
  `*BinaryOp` and `*CaseExpr` included.
- (a) two `*SubqueryExpr`/`*ExistsExpr`/`*InExpr` args carrying different
  plans: previously one key, now one opaque key **per node pointer**.

Laxer (former false negatives, now equal) — the PG-faithfulness fixes:

- (b) structurally identical expressions of any type with `Expr` children
  (previously compared by nested pointer address).
- (b) childless literals at different `pos` (previously compared including
  `pos`).
- (b) `*FuncCall` differing only in identifier case.
- (a) `*ColumnRef` with equal `Index` but unequal `SourceTableIdx` (§3.2).

Every laxer case makes goopg agree with `equal()` in PG, and each one relaxes a
check that PG does not make. The `42P10` path is the only place a laxer answer
changes an error into an acceptance, and PG accepts those queries.

## 5. Gates

- `internal/planner/expr_identity_test.go` — the collision pins. Each is a
  test that FAILS at `da6d2c0c` and passes after: two `*CaseExpr`s / two
  `*CastExpr`s / two `*BinaryOp`s keying apart, structural equality through
  pointer-holding children, `pos`-insensitivity, `scopeVeto`-vs-`scopeIgnore`,
  pointer-identity short-circuit, and an unenumerated type keying **uniquely**
  rather than by type name.
- **The sibling-agreement pin** — `exprEqual(a,b)` ⟺
  `planExprContentKey(a) == planExprContentKey(b)` over a shared pair table.
  §2's divergence was possible only because nothing compared the two
  functions; this test is what makes them a pair.
- **Value gate, not row count.** `TestSharedStateSlotSeparatesDistinctArgs`
  registers a user aggregate and asserts the planner hands two
  `*CaseExpr`-argument calls **different** `SharedStateSlot`s. A row-count gate
  cannot see this: the wrong answer has the right shape (see the SF0.5 oracle's
  blindness to Q87 in `M0125-0006`).
- `exprwalk_exhaustive_test.go` gains `exprSelfKey` in both set-equality
  directions and in the not-vacuous fixture; `exprwalk_inventory_test.go` loses
  the two `walkerPending` pins and gains `exprwalk.go:exprSelfKey` as a
  primitive (census 64 → 63 sites, RC-1a class 50 → 48).
- Units suite, `make plan-diff LABEL=tpcds-round2-head MODE=structural`
  (a plan-shape change is possible in principle: a laxer `pathKeyEqual` can
  merge pathkeys), and the pre-commit pgbench smoke.

### 5.1 The executor half, added 2026-07-30 (both directions MEASURED)

§6's "an end-to-end `CREATE AGGREGATE` value test" and §4's unmeasured
`42P10` claim are both discharged by
`internal/executor/agg_state_sharing_value_test.go`. The planner pin above
asserts where the defect is *decided*; this file asserts where it becomes a
wrong answer, and the two consequences turn out to be **user-visible in both
directions**:

| pin | at `da6d2c0c` | at HEAD | PG 18.3 |
|---|---|---|---|
| `ua_sum(a + b), ua_sum(a - b)` | `(77, 77)` | `(77, -63)` | `(77, -63)` |
| `ua_sum(CASE…a), ua_sum(CASE…b)` | `(3, 3)` | `(3, 30)` | `(3, 30)` |
| sfunc invocations, 3 rows × 2 unshared calls | 3 | 6 | 6 |
| `DISTINCT ON (CASE…) … ORDER BY CASE…` | **`42P10`** | 3 rows | 3 rows |

Three things this arm establishes that the planner-level pin could not:

1. **The wrong answer is reachable end to end, and its shape is exactly as
   argued.** `(77, 77)` is one row of plausible numbers — the second column
   silently echoes the first — so no row-count gate anywhere in this programme
   could have caught it.
2. **The side effects are observable, which is how M0097-0032 was originally
   found.** The fixture's sfunc is a real plpgsql function that `RAISE
   NOTICE`s once per invocation, reaching `ctx.Notices` through
   `executeSFuncCall`'s stored-routine fallback
   (`operators_join_agg.go:3633`), so the test counts sfunc calls rather than
   inferring them. A collision halves the count; an over-strict identity would
   double it, which is what `TestUserAggSharesStateForEqualArgs` guards from
   the other side — the M0097-0035 sharing optimisation must survive.
3. **The laxer direction removed a real spurious error, not a theoretical
   one.** §4 argued from PG's `equal()` ignoring location; measured against
   the oracle (PG 18.3 on `127.0.0.1:65438`), goopg at `da6d2c0c` **rejected**
   `SELECT DISTINCT ON (CASE …) … ORDER BY CASE …` with
   `42P10: SELECT DISTINCT ON expressions must match initial ORDER BY
   expressions`, a statement PG accepts and answers with the three rows HEAD
   now returns. The `%T%v` fallback printed the two structurally identical
   `*CaseExpr` nodes' nested pointers as addresses, so they never matched.
   That makes the fix a **wrong-error fix as well as a wrong-answer fix**.

The fixture reaches the leader/follower copy only because both halves are
user-defined: `buildAggregate` leaves `SharedStateSlot = -1` for built-in
aggregates, and only a user-defined sfunc can be observed running at all.

## 6. Deliberately not done

- **A full TPC-DS SF0.5 value sweep.** Owed (ledger, 2026-07-30) and shared
  with `M0125-0002` commit 1's arm; the `ci/batch` nightly held the host for
  this loop as well (load ~9, its TPC-DS stage at ~11 GB RSS on 65435). A
  concurrent 99-query sweep would both risk the memory guard and corrupt the
  `TIMEOUT` cells the comparison reads.
- **The 46 remaining `walkerPending` sites.** Unchanged scope; `M0125-0002`
  owns seven of them by blast radius.
- ~~**An end-to-end `CREATE AGGREGATE` value test** through the executor's
  leader/follower copy.~~ **DONE 2026-07-30 — see §5.1.** It was cheaper than
  the deferral assumed: `executeSFuncCall` already falls back to
  `executeStoredRoutine`, so a plpgsql sfunc needed no new fixture machinery,
  and the same file closed the unmeasured `42P10` half against the PG oracle.
