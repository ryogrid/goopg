# 0119-0004 — `SET CONSTRAINTS` runtime constraint-deferral control (M0119-0004)

Status: accepted

## Problem

goopg already enforces `DEFERRABLE INITIALLY DEFERRED` foreign keys by queueing
each violated check in `BasicSession.deferredFKChecks` and running them at
`COMMIT` (`runAllDeferredFKChecks`, design lineage M0096-0011). What was missing
is the *runtime* control PG exposes over that deferral:

```
SET CONSTRAINTS { ALL | name [, ...] } { DEFERRED | IMMEDIATE }
```

Before this change `SET CONSTRAINTS` was swallowed as a wire-level no-op
(`compatNoopCommandTag` in the parse-error branch of the simple-query
dispatcher), so:

- a `DEFERRABLE INITIALLY IMMEDIATE` FK could **never** be deferred at runtime,
  and
- `SET CONSTRAINTS ALL IMMEDIATE` could not force an early check of a pending
  deferred violation.

This is the "deferred-constraint-checking-at-COMMIT engine gap" the M0119-0004
triage surfaced (alongside the now-closed NULLS NOT DISTINCT work).

## PostgreSQL semantics (mirrored)

`postgres/src/backend/commands/trigger.c` `SetConstraintStateUpdate` /
`AfterTriggerSetState`, and the SQL reference for `SET CONSTRAINTS`:

- Only affects constraints declared `DEFERRABLE`. A `NOT DEFERRABLE`
  constraint is unaffected — `SET CONSTRAINTS` never makes it deferrable.
- The setting lasts for the **current transaction only** and resets at every
  transaction boundary.
- `... IMMEDIATE` makes the named (or all) deferrable constraints immediate and
  **runs any already-pending deferred checks right away**; a violation raises at
  that point, not at COMMIT.
- The most recent `SET CONSTRAINTS` wins; a later `ALL` supersedes earlier
  per-name settings, a later per-name setting overrides `ALL` for that name.
- Outside an explicit transaction block the command has no lasting effect (the
  surrounding single-statement transaction ends immediately).

goopg's deferred-check infrastructure only tracks **foreign keys** (it has no
deferred UNIQUE/EXCLUDE/CHECK queue), so this slice scopes the runtime control
to FK constraints. UNIQUE/EXCLUDE deferral via `SET CONSTRAINTS` is a documented
follow-up.

## Design

### Session state (`internal/executor/session.go`)

`BasicSession` gains two fields, reset by `EndExplicitTransaction`:

- `constraintsAllMode int8` — `0` unset, `1` ALL DEFERRED, `2` ALL IMMEDIATE.
- `constraintDeferral map[string]bool` — per-constraint-name override
  (`true`=deferred).

New methods:

- `SetConstraintsAll(deferred bool)` — records the ALL setting and clears the
  per-name map (ALL supersedes).
- `SetConstraintsNamed(names []string, deferred bool)` — records per-name
  overrides.
- `FKConstraintDeferred(name string, initiallyDeferred bool) bool` — the
  effective decision: per-name override first, then ALL mode, else the
  constraint's declared `INITIALLY DEFERRED` default.
- `TakeDeferredFKChecksMatching(all bool, names []string) []DeferredFKCheck` —
  removes and returns the queued checks a `... IMMEDIATE` must run now.

### Effective-deferred decision (`internal/executor/operators_fk.go`)

A single helper replaces the four open-coded
`fk.Deferrable && fk.InitiallyDeferred && inTx` sites:

```go
func fkCheckDeferred(ctx *Context, fk catalog.ForeignKey) bool {
    if !fk.Deferrable || ctx.Session == nil || !ctx.Session.InExplicitTransaction() {
        return false
    }
    if sess, ok := ctx.Session.(*BasicSession); ok {
        return sess.FKConstraintDeferred(fk.Name, fk.InitiallyDeferred)
    }
    return fk.InitiallyDeferred
}
```

Call sites updated (all in `operators_fk.go`):

- `checkFKInsertForConstraints` (INSERT/UPDATE parent-exists) — queue vs check.
- `enforceFKOnDelete` NO ACTION arm — queue vs `assertNoChildRows`.
- the `fkChildWaitForInFlightInsert` skip-guard — skip the in-flight-insert wait
  exactly when the check is deferred (it runs at COMMIT, after concurrent
  inserters have drained).
- the second NO ACTION queue site.

Behaviour is **byte-identical** when no `SET CONSTRAINTS` has run:
`FKConstraintDeferred` returns `fk.InitiallyDeferred` and the guard collapses to
the prior expression.

### `SET CONSTRAINTS ... IMMEDIATE` early check

`setConstraintsOp` (`internal/executor/operators_tx.go`) applies the setting to
the session, then for an `IMMEDIATE` request takes the matching queued checks and
runs them through the existing `runAllDeferredFKChecks(ctx, ...)`. A violation
surfaces as a `23503` at the `SET CONSTRAINTS` statement, exactly as PG does.

### Commit-time enforcement in the simple-query path

The simple-query COMMIT path (`dispatch.go`, `case planner.TxCommit`) **bypasses**
`transactionOp.execCommit` and so never ran the queued deferred FK checks — they
were dead in the path psql and the isolation runner use. This change adds an
exported `executor.RunDeferredFKChecks(ctx, sess)` and invokes it there before
`TxnMgr.Commit`, rolling the transaction back with `23503` on a violation.

It is **gated on `sess.ConstraintsOverrideActive()`** (an in-effect SET
CONSTRAINTS override). This activates commit-time enforcement for the SET
CONSTRAINTS-controlled deferral surface this slice owns, while leaving a plain
`DEFERRABLE INITIALLY DEFERRED` constraint's existing simple-query behaviour
untouched. The reason for the gate: activating the check unconditionally
regresses the pass-required `fk-snapshot` isolation spec — its deferred check at
commit must see a *concurrently-committed, partitioned* parent row, which
goopg's `fullTableFKCheck` (transaction `ctx.Snap` + per-partition descent)
does not yet reproduce. That snapshot-semantics work (PG's deferred RI runs with
a fresh "latest" snapshot) is a separate, pre-existing gap recorded in the
deferral ledger; the gate keeps this slice from depending on it.

### Parser / planner / wire wiring

- `parser.SetConstraintsStmt{All, Names, Deferred}` parsed inside `parseSet`
  (`SET CONSTRAINTS { ALL | name[,…] } { DEFERRED | IMMEDIATE }`). `deferred`/
  `immediate` are unreserved idents (matched with `acceptIdentKeyword`, same as
  the `INITIALLY DEFERRED` trailer); `ALL` is the reserved `KwAll`.
- `planner.Plan` wraps it in `Utility` (it joins the existing
  `SetStmt`/`SetTransactionStmt` case).
- `executor.Build` dispatches `*parser.SetConstraintsStmt` to `setConstraintsOp`.
- `commandTagFor`→`utilityTag` returns `SET CONSTRAINTS`.
- Simple-query routing: `query.go` adds a `SET CONSTRAINTS ` case before the
  generic `SET ` GUC case so it reaches the executor (the executor's
  `BasicSession`, where the deferral state lives) instead of `handleSet`.
- Extended-query routing: a `SET CONSTRAINTS ` guard returns a correctly-tagged
  no-op (the extended fast path holds only the GUC `SessionRegistry`, not the
  executor `BasicSession`; deferral via the extended protocol is a documented
  limitation — psql and the isolation harness issue `SET CONSTRAINTS` over the
  simple protocol).

## Blast radius

Default path is untouched: with no `SET CONSTRAINTS` in effect, the new helper
reproduces the previous predicate exactly, and a constraint that is not
`DEFERRABLE` can never be deferred. The new session fields are zero-valued and
reset at every transaction boundary.

## Tests

`internal/executor/set_constraints_test.go`:

- `SET CONSTRAINTS ALL DEFERRED` defers a `DEFERRABLE INITIALLY IMMEDIATE` FK so
  an out-of-order INSERT (child before parent) succeeds and the violation is
  raised at COMMIT.
- the same insert order without `SET CONSTRAINTS` fails immediately (`23503`).
- `SET CONSTRAINTS ALL IMMEDIATE` after a deferred violation raises at the SET
  statement, not at COMMIT.
- `FKConstraintDeferred` precedence (per-name over ALL over declared default).

Parser: `internal/parser/set_constraints_test.go` for the four grammar shapes.

## Follow-ups

- **Deferred-RI snapshot semantics in the simple-query commit path** (the gate
  above): make `fullTableFKCheck` run with a fresh "latest" snapshot so a
  concurrently-committed / partitioned parent row is visible, then drop the
  `ConstraintsOverrideActive()` gate so a plain `INITIALLY DEFERRED` constraint
  is also enforced at commit on the simple-query path without regressing
  `fk-snapshot`. Ledgered.
- Deferred UNIQUE/EXCLUDE constraints (`SET CONSTRAINTS` over a non-FK
  deferrable constraint) — needs a deferred uniqueness queue goopg does not have.
- Extended-protocol deferral (thread the executor session into the extended
  utility fast path).
