# M0100-0005a — IsolationRunner BOOL wire-text reversal

Status: accepted (2026-05-14)
Owner: M0100-0005 — RC isolation 21-spec pass

## Problem

`TestPort_IsolationInsertConflictDoUpdate3` (and any other spec whose
SELECTed columns include `boolean`) showed boolean column values rendered
as `true` / `false` in goopg's runner output, while upstream
PostgreSQL's expected output uses the wire-text form `t` / `f`. The diff
masked progress on the actual ON CONFLICT runtime work.

## Root cause

Three independent layers are involved:

1. **goopg server side.** `internal/server/dispatch.go` formats BOOL
   columns via `Datum.AppendValueText`, which emits the single byte
   `'t'` or `'f'` — wire-format-correct, matches upstream's `boolout`.
   Confirmed by direct hex inspection: a `SELECT ia FROM bw` returns
   a 1-byte DataRow cell containing `'f'` (verified during diagnosis,
   not committed).

2. **lib/pq client side.** `lib/pq@v1.12.3/encode.go:117` decodes
   wire-text BOOL via `return s[0] == 't', nil`, yielding a Go `bool`
   value. The OID is correctly advertised as 16 in
   `internal/server/dispatch.go::typeOIDFor`.

3. **database/sql convertAssign.** When the IsolationRunner scans into
   `sql.NullString`, `database/sql`'s `convertAssign` renders Go
   `bool(false)` as the string `"false"` (and `bool(true)` as `"true"`)
   using Go's default `fmt.Sprintf("%v", ...)`. The wire bytes are
   gone by the time the runner sees them.

Upstream `isolationtester` uses libpq's `PQprint`, which writes the raw
wire bytes verbatim — that path bypasses (2) and (3), so upstream sees
`t` / `f` directly.

## Fix

`runResultRow` in `internal/testport/framework/isolation_runner.go`:

1. Inspect `rows.ColumnTypes()`; mark columns whose
   `DatabaseTypeName() == "BOOL"`.
2. After scanning each row, run captured BOOL-column strings through
   `normalizeBoolWireText`, which maps `"true"` → `"t"` and
   `"false"` → `"f"` and leaves anything else untouched.

The conversion is scoped to BOOL columns identified via the wire OID
mapping (`oid.T_bool = 16`), so non-bool columns whose value happens
to be the string literal `"true"` are unaffected.

### Why not fix this on the server

The goopg server is already emitting the correct wire bytes. The drift
is entirely in pq → database/sql → the test harness's scan path. Adding
a server-side workaround would be wrong on the wire (PostgreSQL clients
that rely on `s[0] == 't'` for BOOL detection would still see the
single-byte form). Fixing it at the harness preserves byte-for-byte wire
compatibility while restoring PQprint-equivalent output for the test
suite.

### Why not switch the runner to `sql.RawBytes`

`*sql.RawBytes` still goes through `convertAssign`, which formats the
typed Go `bool` value via `fmt`. The conversion is irreversible at the
SQL layer once pq has decoded the wire bytes into a typed Go value;
the only way to bypass it is to skip pq entirely and read the wire
directly, which is well out of scope for a test-harness change.

## Regression pins

- `internal/testport/framework/isolation_test.go::TestNormalizeBoolWireText` —
  table-driven over `true`/`false`/`t`/`f`/empty/`True` to pin the
  exact mapping (and document that already-wire-form values pass
  through unchanged).
- `internal/testport/isolation_port_test.go::TestPort_IsolationInsertConflictDoUpdate` —
  must continue to PASS (regression guard for the M0100-0002 / -0003
  / -0004 stack that already exercises BOOL-free ON CONFLICT specs).
- `internal/testport/isolation_port_test.go::TestPort_IsolationInsertConflictDoNothing` —
  same regression guard.

## Observed effect

Before:

```
L18 expected: "  1|Red  |f"
L18 actual:   "  1|Red  |true"
```

After (BOOL-column diff is gone; other diffs from data-modifying CTEs
and `<waiting ...>` remain — those are tracked separately under
M0100-0005's residual work):

```
L18 expected: "  1|Red  |f"
L18 actual:   "  1|Red  |t"
```

## Out of scope

- INSERT-via-CTE support
  (`WITH t AS (INSERT ... ON CONFLICT ...) ...`). This blocks
  `insert-conflict-do-update-3` and is tracked in M0100-0005's
  residual list.
- `<waiting ...>` step-output for ON CONFLICT row-wait. Owned by
  M0100-0003's epqWait wiring; no relation to BOOL formatting.
- Column-width alignment differences for partition-key-update specs.
  Owned by `pqprintFormat`'s alignment mode; no relation to BOOL.
