# regproc/regprocedure OID→name resolution at query output

- Status: accepted
- Date: 2026-07-01
- Milestone: M0119-0004 (deferral-ledger backlog consumption; pg_dump 002-010 TAP catalog-view parity umbrella)
- Supersedes: none
- Closes: the loop #61/#62 deferral-ledger row's resume point — "goopg has no
  general OID→name resolution for `regproc`-typed columns at query-output
  time in *either* protocol"

## Problem

Real PostgreSQL's `regprocout`/`regprocedureout` (`src/backend/utils/adt/regproc.c`)
render a `regproc`/`regprocedure` value as the referenced function's `proname`
text on any direct `SELECT`, not just under an explicit `::regproc` cast.
Several built-in catalog columns are declared `regproc` and store a raw
`pg_proc` OID:

- `pg_type.typinput`/`typoutput`/`typreceive`/`typsend`/`typmodin`/
  `typmodout`/`typanalyze` (`internal/initdb/pg_type_bootstrap.go`)
- `pg_operator.oprcode`/`oprrest`/`oprjoin` (`internal/initdb/pg_operator_bootstrap.go`)
- `pg_am.amproc`/`pg_amproc.amproc` (`internal/initdb/initdb.go`)

Before this change, none of these rendered a name:

1. **Output formatting.** Neither `internal/server/dispatch.go`'s nor
   `dispatch_extended.go`'s per-column type-formatting switch
   (`appendTypedCellText`, unified across both wire protocols by
   `0119-0004-extended-protocol-type-format-parity.md`) had a `regproc`/
   `regprocedure` case, unlike the existing `regclass` case. A direct
   `SELECT typinput FROM pg_type` therefore fell through to the generic
   `Datum.AppendValueText`, rendering the bare numeric OID.
2. **Cast evaluation.** `internal/executor/expr.go`'s `CastExpr` handling of
   `::regproc`/`::regprocedure` special-cased `InvalidOid` (0 → `"-"`) but,
   for any other OID, returned the input datum unchanged — a silent no-op
   cast that also rendered the raw number once the result reached output.

This was flagged but explicitly out of scope in the two prior loops
(`0119-0004-*regproc-columns-render-names` for `pg_aggregate`'s BKI rows,
`0119-0004-extended-protocol-type-format-parity.md` for the sibling wire-
protocol-divergence fix) because each of those was scoped to a narrower,
already-in-flight gap; this doc closes the general mechanism both of them
deferred.

## Why a new leaf package

`internal/initdb/pg_aggregate_view.go` already carries a private
`pgProcNameForOID` (OID→proname index over the generated 3397-row
`pgProcAllEntries()`, `internal/initdb/pg_proc_seed_data.go`) used only for
the built-in `pg_aggregate` BKI rows. Reusing it directly from
`internal/executor` or `internal/server` is impossible: `internal/initdb`
already imports `internal/executor` (for `executor.Datum` in virtual-row
builders), so the reverse import would cycle. `internal/server` *could*
import `internal/initdb` (no cycle there — see `basebackup.go`), but
`internal/executor` cannot, and the cast-evaluation fix in `expr.go` needs
the same index.

Mirrors the "Version constants must live in leaf config pkg" precedent:
the index moves to `internal/catalog`, which both `internal/executor` and
`internal/initdb`/`internal/server` already import, and which imports
neither (`internal/catalog` only pulls in `internal/parser`,
`internal/sqlkeywords`, `internal/storage` — a true leaf).

## Implementation

1. **Generator** (`cmd/gen-pg-proc-data/main.go`): new `-names` flag. Instead
   of emitting the full `pgProcEntry` table (arg types, handler names, ...),
   it emits a name-only `map[uint32]string` literal into `package catalog`:

   ```
   go run cmd/gen-pg-proc-data/main.go -names > internal/catalog/pg_proc_names_generated.go
   ```

   This is a duplicated (name-only) copy of the same generated source, not a
   hand-curated second table — regenerating both files together keeps them
   from drifting when `postgres/src/include/catalog/pg_proc.dat` changes.

2. **`internal/catalog/pg_proc_names_generated.go`** (generated): the 3397-
   entry `pgProcNamesByOID` map.

3. **`internal/catalog.RegprocName(oid uint32) (string, bool)`**
   (`catalog.go`, next to `BuiltinProcs`/`LookupBuiltinProc`): the reverse
   lookup. `ok=false` means `oid` is not a known built-in — a *user-defined*
   function OID (`CREATE FUNCTION`) isn't in this table at all, since it's
   assigned at runtime by the live routine registry, not baked into the
   generated BKI snapshot.

4. **`internal/server/dispatch.go`**: `appendTypedCellText` gains a
   `case "regproc", "regprocedure"` mirroring the existing `regclass` case —
   `oid == 0` → `"-"`; else try `catalog.RegprocName`, then fall back to
   `s.cfg.Catalog.Routines().LookupByOID(oid)` for a user-defined function;
   an OID resolved by neither source (should not occur for any OID a real
   BKI column references) falls back to the raw numeric text, same
   defensive convention as `aggBuiltinFuncName`. Both wire protocols share
   this one switch (post-0119-0004bi), so the fix is automatically visible
   to Parse/Bind/Execute as well as simple-query.

5. **`internal/executor/expr.go`**: the `::regproc`/`::regprocedure`
   `CastExpr` OID-input branch now resolves a non-zero OID the same way
   (`catalog.RegprocName` then `ctx.Catalog.Routines().LookupByOID`) instead
   of returning the input datum unchanged, matching how the sibling
   `::regclass` OID-input branch already resolves eagerly to a `KindString`.

## Scope / non-goals

- `regoper`/`regoperator` (operator-name OID types) are a separate,
  unaddressed gap — no column in goopg's virtual/heap catalog views is
  currently typed `regoper`, so there is no observable regression surface
  for it yet.
- Overload resolution: `Routines().LookupByOID` returns the bare name with
  no signature disambiguation, matching every other OID→name path in this
  codebase (`aggFuncNameOrDash`, the `regclass` case). PG's
  `regprocedureout` normally also renders the argument-type list
  (`foo(integer,text)`); goopg's `regprocedure` case renders the bare name
  like `regproc` does, since no current fixture exercises an overloaded
  `regprocedure` value on a direct query — flagged, not fixed, here.

## Gates

- `go build ./...` / `go vet ./...` clean.
- New unit tests: `TestRegprocName` (`internal/catalog`),
  `TestAppendTypedCellTextRegprocRendersName` (`internal/server`),
  `TestRegprocOIDCastResolvesName` (`internal/executor`).
- `internal/catalog`, `internal/executor`, `internal/server` full suites PASS.
- `internal/initdb`, `internal/planner` full suites PASS (siblings that read
  the touched catalog views / cast path).
- TPC-H spot-check Q12=2/Q13=33 PASS.
- pgbench smoke = pre-commit hook.
