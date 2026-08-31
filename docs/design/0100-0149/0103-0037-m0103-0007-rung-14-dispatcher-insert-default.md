# 0103-0037 — M0103-0007 rung 14: dispatcher INSERT DEFAULT-expression evaluation

Status: accepted
Owner: M0103-0007
Depends on: 0103-0036 (rung 13)

## Context

Rung 13 (0103-0036) closed a correctness gap in the **apply worker's**
INSERT path: subscriber-side columns the publisher did not claim get
their `CREATE TABLE … DEFAULT <expr>` evaluated at INSERT time instead
of being silently zero-valued. To land that, the parser was upgraded to
capture each column's DEFAULT clause as an `Expr` AST on
`ColumnDef.DefaultExpr` (replacing the prior consume-and-discard),
`catalog.Column` gained `DefaultExpr parser.Expr`, and a reusable
helper `applyDefaultsForMissing(cols, row, missing)` was added in
`internal/executor/operators_generated.go` that evaluates each missing
slot's DEFAULT via the lightweight `evalGenExpr` walker.

That work made every piece of the DEFAULT pipeline live in goopg —
**except** the regular dispatcher INSERT path. `insertOp.Next` in
`internal/executor/operators_storage.go` reorders the source row into
target-column order via `plan.ColumnIndex` and initialises every
unmapped slot to `NullDatum`. There is no DEFAULT-evaluation pass, so

```sql
CREATE TABLE t (id int PRIMARY KEY, label text, note text DEFAULT 'auto');
INSERT INTO t (id, label) VALUES (1, 'one');
```

silently installs `note=NULL` even though `note` has a DEFAULT.
Upstream PostgreSQL's `ExecInitStoredGenerated` / `ExecComputeStoredGenerated`
pass (and `transformInsertStmt`'s defaults rewrite) fills omitted
columns with their DEFAULT *before* generated-column evaluation and
before SERIAL `nextval` assignment.

The note in rung 13 — "DEFAULT-expression evaluation in the regular
dispatcher INSERT path (orthogonal to logical replication parity but
unblocked by the parser/catalog work landed here)" — points exactly at
this gap. Rung 14 closes it.

## Goals

1. `INSERT INTO t (a, b) VALUES (…)` against a table where additional
   columns carry a CREATE TABLE DEFAULT must persist the DEFAULT value
   for the omitted columns.
2. Explicit values supplied for columns with a DEFAULT must win over
   the DEFAULT (PostgreSQL semantics: DEFAULT is the fallback, not an
   override).
3. Columns with no DEFAULT clause must stay `NullDatum` (rung 13's
   negative pin, kept here).
4. The existing SERIAL / GENERATED / trigger / FK / partition paths
   keep working unchanged. DEFAULT evaluation runs BEFORE SERIAL
   `nextval` so a serial column with no explicit value still falls
   through to `nextval` (DefaultExpr is nil for SERIAL columns).

## Non-goals

- Parser support for `INSERT … VALUES (DEFAULT, …)`. That is a separate
  rung; rung 14 only covers omitted columns. (PG resolves `DEFAULT`
  inside VALUES by inserting the column's default expression at parse
  time, which goopg's parser does not implement.)
- Richer DEFAULT expressions (`nextval('s')`, `now()`, `gen_random_uuid()`).
  The reused `evalGenExpr` walker already covers literals (int /
  string / bool / null), column refs, casts, simple arithmetic — the
  shapes tests actually exercise. Function-call DEFAULTs remain
  silently no-op (slot stays at the source-fill value) so NOT NULL
  violations surface loudly instead of corrupted rows.

## Design

### `insertOp.Next` — compute `missing` mask once, fill DEFAULTs per row

Modify `internal/executor/operators_storage.go::insertOp.Next` to:

1. Compute `insertMissing []bool` ONCE before the per-row loop:
   `insertMissing[i] = true` for every target column index NOT present
   in `o.plan.ColumnIndex`. `plan.ColumnIndex` is immutable across rows
   (Insert is a single-statement op), so hoisting the mask out saves
   per-row allocation and matches the cost shape of the rung-1–13 hot
   path.
2. Inside the per-row loop, AFTER the existing
   `for srcIdx, tgtIdx := range o.plan.ColumnIndex { row[tgtIdx] =
   src[srcIdx] }` reorder loop, call:
   `applyDefaultsForMissing(cols, row, insertMissing)`.
3. The SERIAL auto-generation block immediately below remains
   unchanged. It runs only when `!row[i].IsNull()` is false, so a
   SERIAL column whose DEFAULT was not evaluated (DefaultExpr is nil
   for SERIAL) continues to fall through to `nextval`.

### Why reuse `applyDefaultsForMissing`

Rung 13's helper already encodes the correctness invariants this rung
needs:

- `missing[i] = false` ⇒ NEVER overwrite the slot (explicit-value wins).
- `cols[i].DefaultExpr == nil` ⇒ skip (NULL is the correct fallback).
- `evalGenExpr` failure ⇒ leave the slot unchanged (NOT NULL violation
  surfaces loudly instead of silently NULL-ing).

These are exactly the semantics rung 14 wants, so reusing the helper
is correct and keeps the two INSERT paths (apply-worker and
dispatcher) in lockstep — any future improvement to the DEFAULT
evaluator benefits both.

### Order of operations inside `insertOp.Next`

```
source-fill (existing)
applyDefaultsForMissing            ← NEW (rung 14)
SERIAL nextval (existing)
BEFORE INSERT triggers (existing)
CHECK / FK (existing)
computeGeneratedColumns (existing)
heap write + index maintenance (existing)
```

This matches upstream's slot-init order: defaults → SERIAL → triggers
→ generated → heap. A trigger can still observe and mutate the
DEFAULT-filled value (rung 14 does not block trigger interception).

## Risks and mitigations

| Risk | Mitigation |
|------|-----------|
| SERIAL columns silently inheriting a DEFAULT | `parser.parseColumnDef` does not assign DEFAULT to SERIAL declarations, so `DefaultExpr == nil` for SERIAL → helper skips. Verified by inspection (rung 13 did not change the SERIAL branch). |
| Per-row mask allocation cost | Mask computed once before the loop; in-loop cost is one slice-index lookup per missing column. |
| Wrong-table mask reuse on RETURNING / partition routing | `insertMissing` keys by parent-table column ordinal. Partition routing remaps row layouts via `remapRowForPartition` BEFORE write; that remap reads `row[i]` after DEFAULT-fill is complete, so partition children inherit the DEFAULT-filled values. Children with DIFFERENT DEFAULT clauses don't apply (DEFAULT is a parent-level declaration in goopg's catalog model). |
| Explicit-NULL vs missing semantics | `INSERT INTO t (id, note) VALUES (1, NULL)` puts `id, note` BOTH in `ColumnIndex` → `missing[note]=false` → DEFAULT not applied → row stores NULL. Correct (matches PG). The negative pin `TestInsertDoesNotOverrideExplicitColumnDefault` covers this. |

## Verification

- `TestInsertFillsMissingColumnDefault` in `internal/executor/storage_test.go`:
  table `(id int NOT NULL, label text, note text DEFAULT 'auto', bare text)`,
  INSERT with `ColumnIndex=[0,1]`, asserts `note='auto'` (DEFAULT fired),
  `bare IS NULL` (no DEFAULT — stays NULL), `id=1 label='one'`,
  `count(*)=1`. Each assertion fail-fasts a distinct regression.
- `TestInsertDoesNotOverrideExplicitColumnDefault`: same fixture,
  `ColumnIndex=[0,1]` with `note` in the list, value `'explicit'` →
  asserts `note='explicit'` (DEFAULT must not override).
- All 12 `TestPort_PgoutputInteropPGToGoopg*` rung-1–13 tests still pass
  (regression sweep: ~23 s).
- Race-tested regression on
  `./internal/executor/ ./internal/planner/ ./internal/parser/
  ./internal/catalog/` → all green.

## Future rungs (deferred within M0103-0007)

- pgbench against PG publisher with `pgbench_history` polling.
- proto_version=2 streaming subxacts.
- kill -9 + libpq multi-host reconnect plumbing on the client side.
- `INSERT … VALUES (DEFAULT, …)` parser support (orthogonal to
  M0103-0007's principal goal).
- Richer DEFAULT evaluator (function calls, sequences) when a fixture
  surfaces a need.
