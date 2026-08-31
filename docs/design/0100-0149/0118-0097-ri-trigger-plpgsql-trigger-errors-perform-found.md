# 0118-0097 — `ri-trigger.spec` PROMOTED: plpgsql trigger-body errors abort the DML + `PERFORM` query form + `FOUND`

- **Milestone:** M0118-0009 (Upstream Isolation Spec Suite Pass-Through)
- **Spec:** `postgres/src/test/isolation/specs/ri-trigger.spec`
- **Status:** accepted
- **Outcome:** `ri-trigger.spec` `failed`→`pass`, all 10 permutations byte-identical
  vs PostgreSQL 18.3; promoted to `runIsoSpecStrict` in
  `TestPort_IsolationRiTrigger`.

## What the spec exercises

`ri-trigger.spec` implements referential integrity *with user triggers* (not
declarative FKs) under `SERIALIZABLE`, and asserts that any overlap between the
two transactions yields a serialization failure:

- `parent` has a `BEFORE UPDATE OR DELETE … FOR EACH ROW` plpgsql trigger
  `ri_parent()` that runs `PERFORM TRUE FROM child WHERE parent_id = OLD.parent_id;`
  and, `IF FOUND`, raises `SQLSTATE '23503'` (`child row exists`).
- `child` has a `BEFORE INSERT OR UPDATE … FOR EACH ROW` plpgsql trigger
  `ri_child()` that runs `PERFORM TRUE FROM parent WHERE parent_id = NEW.parent_id;`
  and, `IF NOT FOUND`, raises `SQLSTATE '23503'` (`parent row missing`).
- The 10 permutations interleave a child INSERT (s1) with a parent DELETE (s2),
  expecting either the trigger error (`child row exists` / `parent row missing`)
  or the SSI `40001` (`could not serialize access due to read/write
  dependencies among transactions`) depending on commit order.

goopg already had the SSI `40001` machinery (M0104) and a plpgsql trigger
runtime (used by `eval-plan-qual-trigger`, 0118-0095). Probing the spec
nonetheless landed at the very first divergence (permutation 1, L13): goopg's
parent DELETE *succeeded silently* where PG raised `child row exists`.

## Root causes (three independent gaps)

### 1. Trigger-body errors were swallowed

`fireTriggers` (the single entry point for BEFORE/AFTER row triggers used by
every DML operator) caught the error from `executePLpgSQLTriggerBody` and
`continue`d with a literal comment: *"For now, silently skip — production should
propagate."* So a `RAISE` inside a trigger never aborted the DML — the row was
written anyway and no error reached the client. This is the dominant bug: it
silently discards trigger-enforced constraints across INSERT/UPDATE/DELETE/
MERGE/upsert.

**Fix:** `fireTriggers` now returns `(Row, bool, error)` and returns the error
instead of swallowing it. All ~21 call sites in `operators_storage.go`,
`operators_merge.go`, and `operators_upsert.go` propagate it (returning
`(nil, err)` from the operator `Next`, or `err` from the closures / helper
functions `mergeApplyUpdate`/`mergeApplyDelete`); the two page-locked call sites
release the buffer before returning. AFTER-trigger call sites (previously
discarding all returns) propagate too — a RAISE in an AFTER trigger aborts the
statement in PG. This only ever *adds* an abort where PG already aborts, so it
can only close divergences (any plpgsql gap that errors spuriously would be a
pre-existing latent issue, not introduced here — verified by the full
trigger/DML spec batch below).

### 2. `PERFORM` only accepted a scalar expression

`parsePerform` parsed the text after `PERFORM` as a single expression
(`parser.ParseExpr`), so the query form `PERFORM TRUE FROM child WHERE …` failed
with a plpgsql parse error (*"unexpected trailing tokens after expression (got
from)"*). In PostgreSQL, `PERFORM query` runs the rest as `SELECT <query>`,
discards the rows, and sets `FOUND`.

**Fix:** `parsePerform` now captures the raw source up to the terminating `;`
into `PerformStmt.Query`, and *additionally* keeps the parsed `Expr` when the
source happens to parse as a plain expression (the common `PERFORM foo()`
case). The runtime keeps the scalar fast path when `Expr != nil` (a scalar
`SELECT` yields one row → `FOUND` true) and otherwise executes
`SELECT <Query>` via the existing `execPLpgSQLEmbeddedSQL` path (which already
substitutes `OLD.*`/`NEW.*` and frame variables), setting `FOUND` from the row
count.

### 3. `FOUND` was not implemented

A bare `FOUND` reference lowered to `frame.lookup("found")`, which failed with
*"variable FOUND does not exist"*. Nothing in the runtime ever set it.

**Fix:** `plpgsqlFrame` gained a `found bool` field. `lowerPLpgSQLExpr`'s
bare-identifier path falls back to a `BooleanConst{frame.found}` for `FOUND`
when it is not shadowed by a declared variable of the same name (declared vars
still win, which is safe). `FOUND` is set from the underlying query's row count
by `PERFORM` and by the embedded-SQL statement path (`SQLStmt`), matching PG's
"FOUND reflects the last SQL statement's row count" semantics. To support this,
`execPLpgSQLEmbeddedSQL` now returns `(int, error)` (rows produced by the last
statement).

## Blast radius

- The `fireTriggers` signature change is internal to `internal/executor`; the
  behavioural change is "trigger RAISE now aborts the DML", which is strictly
  PG-faithful and only fires when a trigger body errors (no error path is taken
  by trigger bodies that previously succeeded).
- `PERFORM` keeps its exact prior behaviour for the scalar form (`Expr != nil`);
  the new SQL path only runs for query forms that previously failed to parse.
- `FOUND` was previously unreadable (hard error), so making it readable and
  setting it is purely additive — no passing test could have depended on the
  prior error.

## Gates

- `TestPort_IsolationRiTrigger` strict **PASS** (all 10 permutations byte-identical).
- Trigger/DML-using isolation specs PASS, no regression:
  `EvalPlanQualTrigger`, `CreateTrigger`, `PartitionKeyUpdate4`,
  `Merge{Update,Delete,InsertUpdate,MatchRecheck,Join}`,
  `InsertConflictDoUpdate`, `InsertConflictDoNothing`, `InheritTemp`,
  `ReferentialIntegrity`, `FkDeadlock`.
- `internal/plpgsql`, `internal/executor`, `internal/planner`, `internal/server`
  unit suites PASS.
- `TestPort_PLpgSQL*` (procedure/function/expr-context) PASS.
- regress-port DML/trigger cases (`delete`/`insert`/`plpgsql`/`portals`/
  `returning`/`triggers`/`update`) run with no new failure (all pre-existing
  `defer`; `delete` PASS).
- `go build ./...` clean; `go vet ./internal/executor/... ./internal/plpgsql/...`
  clean; pgbench smoke = pre-commit hook.

## Remaining M0118-0005 work

`ri-trigger` was the last user-trigger blocker in the M0118-0005 FK/RI group.
Still deferred there: `fk-partitioned-1/2` (`ALTER TABLE … ATTACH PARTITION` +
partitioned-FK enforcement).
