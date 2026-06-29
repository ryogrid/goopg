(idle — nothing in flight)

Last loop (#18): M0119-0004 **`SET CONSTRAINTS` runtime constraint-deferral
control** — LANDED + design `0119-0004-set-constraints-deferred`. Second general
SQL-engine gap under M0119-0004 (the deferred-constraint-checking-at-COMMIT gap).
- Parser `SetConstraintsStmt` (`SET CONSTRAINTS {ALL|name[,…]} {DEFERRED|IMMEDIATE}`)
  → `planner.Utility` → `executor.setConstraintsOp`.
- `BasicSession`: `constraintsAllMode int8` + per-name `constraintDeferral` map
  (reset per txn); `FKConstraintDeferred(name, initiallyDeferred)` precedence
  per-name > ALL > declared default; `TakeDeferredFKChecksMatching` (IMMEDIATE).
- New `fkCheckDeferred(ctx, fk)` helper replaces the 4 open-coded
  `Deferrable && InitiallyDeferred && inTx` sites in operators_fk.go
  (byte-identical with no override).
- IMMEDIATE runs queued checks at the SET stmt (setConstraintsOp). Simple-query
  COMMIT path (bypasses execCommit) gained `executor.RunDeferredFKChecks`
  **GATED on `ConstraintsOverrideActive()`** — plain INITIALLY DEFERRED keeps
  prior behaviour (unconditional activation REGRESSED pass-required `fk-snapshot`:
  its deferred-RI check needs a FRESH snapshot to see a concurrently-committed
  *partitioned* parent; `fullTableFKCheck` uses txn `ctx.Snap` → false 23503).
- query.go simple-query routing + extended no-op + `SET CONSTRAINTS` tag;
  removed old compatNoopCommandTag entry.
- Tests: parser(4 shapes), executor session(precedence/matching), e2e
  `TestPort_SetConstraintsDeferral` (control/ordered/raise-at-COMMIT/
  raise-at-IMMEDIATE via pinned `*sql.Conn`). fk-snapshot + full FK isolation
  group + executor/parser/server units PASS; -race executor PASS; build clean.

NEXT loop — pick topmost actionable M0119:
- M0119-0004 still open: pg_dump 002–010 catalog parity battery (slice-by-slice
  via self-promoting `TestPort_PgDumpConnectionSetup` guard — currently GREEN,
  add a fixture to find the next gap); **deferred-RI fresh-snapshot** to drop the
  ConstraintsOverrideActive gate (ledger row, resume = fresh "latest" snapshot in
  `fullTableFKCheck`/`assertParentExists`, verify fk-snapshot stays green).
- M0119-0002 (CLOG store swap Part B) — highest blast radius, dedicated full-gate.
- M0119-0005/0006/0007 blocked (index AMs / logical decoding).
