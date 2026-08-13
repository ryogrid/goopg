# 0119-0006at — TEXT/CSV COPY of a `reg*` column renders its name (M0119-0006, 68th slice)

## Problem

Deferral ledger row 1303 (2026-08-14, 67th slice). A `reg*` column now holds a
4-byte OID on the heap end to end (heap codec, binary COPY, pgoutput), but the
**TEXT/CSV COPY** path still renders its numeric OID in both directions; only
binary COPY / INSERT / SELECT are PG-faithful.

- **COPY TO** — `datumToCopyText` (`internal/executor/copy_text.go`, shared by
  the TEXT row writer `EncodeCopyTextRow` and the CSV writer `EncodeCopyCsvRow`)
  has no `reg*` arm, so a `regrole` column copies OUT as `10` where PG's
  `regroleout` emits `postgres`, and a `regclass` column copies OUT as `1259`
  where `regclassout` emits `pg_type`.
- **COPY FROM** — the row is written through
  `insertSourceRow` → `writeHeapRowReturning` → `EncodeRowPG` directly,
  bypassing `coerceRowForConstraintChecks` (the name→OID choke point the 66th/67th
  slices wired for INSERT/UPDATE). So a name field reaches `encodeValuePG`'s reg*
  arm as a `KindString` and `regIdentifierOIDFromDatum`'s numeric parse runs on
  it — a hard error on `postgres`, a silent numeric parse on `10`.

The numeric OID is lossless cross-engine (`reg*in` accepts a numeric OID), which
is why the 66th slice shipped the same behavior for `regclass`/`regtype`/
`regprocedure`/`cid` without fixing it. This slice closes it for the whole
family.

## What upstream does

Nothing COPY-specific. `CopyOneRowTo` calls the column's output function
(`copyto.c`), `CopyFrom` calls its input function through `InputFunctionCall`
(`copyfromparse.c`). COPY TO of a `regrole` column is `regroleout`; COPY FROM of
a name into one is `regrolein`. The COPY path is therefore the **same name↔OID
contract as SELECT / INSERT** — the correct shape is "COPY TO renders like
SELECT", "COPY FROM coerces like INSERT", and both must use the exact resolvers
those paths already use.

## Design

### COPY TO: one shared `reg*` output renderer for SELECT and COPY

`RunCopyTo` already holds `ctx`. Widen the three text/CSV renderer signatures
with a `cat catalog.Catalog` value and a `qualify bool` (search-path
qualification for `regtype`), following the 48th slice's "parameter rather than
a `*Context`" pattern (docs/design/0125-0007 §19.2):

- `EncodeCopyTextRow(dst, row, cols, dateStyle, dateOrder, timeZone, cat, qualify)`
- `EncodeCopyCsvRow(dst, row, cols, f, dateStyle, dateOrder, timeZone, cat, qualify)`
- `datumToCopyText(t, d, dateStyle, dateOrder, timeZone, cat, qualify)`

`RunCopyTo` passes `ctx.Catalog` and computes `qualify` as
`!executor.regObjectSchemaVisible(ctx, "public")` — the negation is the point:
`regObjectSchemaVisible` returns true when "public" is on the search_path (the
default), in which case a `regtype` name renders unqualified, matching the
executor-side twin `internal/server`'s `publicSchemaVisible` used by the
`appendTypedCellText` regtype case. (Review note: `regObjectSchemaVisible` is
unexported but in the same package, so `RunCopyTo` can call it directly.)

New exported `executor.RegOut(typeName string, oid uint32, cat catalog.Catalog,
qualify bool) string` in `internal/executor/reg_identifier.go` implements every
reg*out (postgres/src/backend/utils/adt/regproc.c) as one switch:

| type | PG function | resolution | dangling |
|---|---|---|---|
| regclass | `regclassout` | `InMemory.LookupTableByOID` / `LookupIndexByOID` | numeric |
| regproc | `regprocout` | `catalog.RegprocName`, then `Routines().LookupByOID` | numeric |
| regprocedure | `regprocedureout` | `catalog.RegprocedureName(oid, routines)` | numeric |
| regtype | `regtypeout` | `executor.RegtypeName(cat, oid, qualify)` (already handles OID 0 → `-`, `unknown`, dangling) | numeric |
| regrole | `regroleout` | `InMemory.RoleNameForOID` | numeric |
| regcollation | `regcollationout` | `InMemory.ResolveIndexColumnCollationName` | numeric |

Every case renders **OID 0 (InvalidOid) as `"-"`** — each reg*out in regproc.c
has the `InvalidOid` guard. A nil `cat` (or a failed `*InMemory` assertion)
falls through to the numeric form, preserving the current no-catalog behavior.

Two resolution details the review surfaced (both preserve the SELECT sibling's
behavior exactly, so `RegOut` reproduces it):
- **`regprocedure`:** `catalog.RegprocedureName(oid, routines)` is nil-safe for
  `routines`; `appendTypedCellText` passes `s.cfg.Catalog.Routines()` when the
  catalog is non-nil and `nil` otherwise (`dispatch.go:3244-3247`). `RegOut`
  mirrors that — nil `cat` → nil routines.
- **`regrole`:** `InMemory.RoleNameForOID` returns the **numeric string** for a
  dangling role (`catalog.go:15948-15961`), not `""` — only OID 0 returns `""`.
  So `RegOut`'s "render `n` whenever non-empty" reproduces PG's numeric
  fallback for a dangling role without a second format.

`appendTypedCellText` (`internal/server/dispatch.go`) is refactored so its six
reg* cases collapse into one call to `executor.RegOut` — one renderer, both
paths, so COPY TO cannot drift from SELECT (Hard-won Rule #2,
`pattern_sibling_paths_must_agree`). This is a **behavioral fix, not just a
consolidation**: the SELECT regclass case has no OID-0 guard and calls
`im.LookupTableByOID(0)`, which — in a live catalog — matches the
information_schema virtual tables registered with OID 0 (`routines`/
`parameters`/`usages`, `catalog.go:11615-11651`), so it renders a
**nondeterministic table name** for OID 0 (Go map iteration), where `regclassout`
and the `::regclass` cast path (`expr.go:776-783`, which carries the OID-0
guard precisely because of that lookup) both render `"-"`. `RegOut`'s OID-0
guard makes all three agree. (Review note: the pre-fix behavior is NOT a stable
`"0"` — any test written against a `"0"` baseline would observe a
nondeterministic name. The sibling-agreement test therefore asserts the
post-refactor invariant: both paths call `RegOut`, so they agree on every OID.)

`datumToCopyText` gains the reg* arm before its type-name switch:

```go
if !t.IsArray && isRegIdentifierTypeName(t.Name) && d.Kind == KindInt {
    return RegOut(t.Name, uint32(d.Int), cat, qualify), nil
}
```

A non-`KindInt` datum (a `KindString` already rendered by a `::reg*` cast
result feeding `COPY (SELECT …) TO`) falls through to the existing default
`KindString` arm, which emits it as-is — the same fall-through the SELECT path
uses.

### COPY FROM: coerce reg* columns in `insertSourceRow`

The decode path (`DecodeCopyTextRow`/`DecodeCopyCsvRow`/`copyTextToDatum`) is
**unchanged**. Instead, `insertSourceRow` (`internal/executor/copy.go`), the one
place both the TEXT and CSV FROM readers converge, routes the scattered row
through the SAME `coerceRowForConstraintChecks` the INSERT path uses, with an
include-filter that admits **only the reg* family**:

```go
if err := coerceRowForConstraintChecks(c.cols, row, func(i int) bool {
    return isRegIdentifierTypeName(c.cols[i].Type.Name)
}, c.ctx, c.plan.Pos()); err != nil {
    return err
}
```

Choosing this over reg* arms inside `copyTextToDatum`:

- **No double-coercion risk.** Every non-reg* column is already typed by
  `copyTextToDatum`; re-running the int/date/numeric arms on typed values is the
  drift risk this filter removes by construction. reg* is the only family whose
  COPY-FROM value is still an untyped `KindString`.
- **Correct SQLSTATE.** `regIdentifierInput`'s errors (42P01 / 42704 / 42883 /
  42602) propagate from `insertSourceRow` through `PushLine` **unwrapped**
  (`copy.go:229` returns the error as-is), exactly as `regclassin`'s error
  surfaces from `CopyFrom` upstream. A `copyTextToDatum` arm would be wrapped in
  22P04 by `PushLine` (`copy.go:227`), like every other input error on that
  path — a pre-existing, separately-tracked divergence not widened here.
- **Identical resolver.** reg* names resolve through `regIdentifierInput` — the
  exact function the INSERT path uses, including `parseRegDashOrOid` (`"-"` →
  OID 0, pure-digit → numeric OID) and the `regrole` 42602 / qualified-name
  cases.

### New helper

`isRegIdentifierTypeName(name string) bool` in `internal/executor/
reg_identifier.go` — the family list (`regproc`, `regprocedure`, `regclass`,
`regtype`, `regrole`, `regcollation`), with `oid`/`cid` (numeric-only) excluded.
**Scope guard (review item 8):** the helper is used at the two NEW sites (the
`datumToCopyText` guard and the `insertSourceRow` filter) and adopted by
`coerceRowForConstraintChecks` (whose inline list at `operators_storage.go:2355`
is exactly these six). It must NOT be substituted at the encode/align arms —
`encodeValuePG`'s oid arm (`codec.go:384`) and `physicalPGTypeAlign`
(`codec.go:1193`) spell a WIDER list that INCLUDES `oid`/`cid`; a mechanical
substitution would silently drop those two from the match and corrupt the heap
path. Those arms stay as-is (splitting `oid`/`cid` out of them is a separate
cleanup, not this slice).

## New / changed symbols

- `internal/executor/reg_identifier.go`: `RegOut` (new, exported);
  `isRegIdentifierTypeName` (new).
- `internal/executor/copy_text.go`: `datumToCopyText` gains `cat`, `qualify`
  params and the reg* arm; `EncodeCopyTextRow`/`DecodeCopyTextRow` signatures
  as above.
- `internal/executor/copy_csv.go`: `EncodeCopyCsvRow` gains `cat`, `qualify`.
- `internal/executor/copy.go`: `RunCopyTo` passes `ctx.Catalog` + computed
  `qualify`; `insertSourceRow` calls `coerceRowForConstraintChecks` for reg*
  columns.
- `internal/server/dispatch.go`: `appendTypedCellText`'s six reg* cases collapse
  into one `executor.RegOut` call.
- Existing callers (tests) gain trailing `nil, false` — nil catalog preserves
  today's numeric rendering.

## Tests

- `internal/executor/reg_copy_test.go` (new):
  - `TestRegCopyToRendersName` — for each of the six reg* types, a `COPY …
    TO STDOUT`-shaped `EncodeCopyTextRow` and `EncodeCopyCsvRow` over an
    `InMemory` catalog render the OID's name (`regrole` 10 → `postgres`,
    `regclass` 1259 → `pg_type`, `regtype` 23 → `integer`, …), where the same
    catalog/datum rendered numeric before the arm.
  - `TestRegCopyToInvalidOidIsDash` — OID 0 renders `"-"` for all six.
  - `TestRegCopyToSiblingAgreement` — `RegOut` (COPY) and `appendTypedCellText`
    (SELECT) produce identical bytes for a matrix of OIDs × types.
  - `TestRegCopyFromResolvesName` — `DecodeCopyTextRow` of `postgres`/`C`/
    `pg_type` into a reg* column, run through `insertSourceRow`-equivalent
    coercion, stores the right OID; a miss raises the family's own error
    (42P01 for `regclass`, 42704 for `regrole`/`regcollation`) **unwrapped**.
  - `TestRegCopyFromNumericAndDash` — `10` stays OID 10 and `-` becomes OID 0
    (parseRegDashOrOid on the FROM path).
- `internal/server/regtype_output_test.go` / `regproc_output_test.go` — existing
  pins must still pass after the `appendTypedCellText` consolidation.
- Full gates per the executor practice card: pre-commit units, regress suite,
  `scripts/tpch-spotcheck.sh`.

## Found + deferred (ledger rows)

- `regclassout`'s **schema-qualification** of a relation not visible on the
  search_path is not ported (`RegOut` returns the bare name; so does the SELECT
  path today). A COPY TO of a reg* column from a non-default schema diverges
  from PG only there. Matches the status quo, recorded. (Review note: the same
  applies to `regprocout`/`regcollationout`/`regroleout`, which upstream also
  schema-qualify and quote via `quote_qualified_identifier` — all inherited
  SELECT-path gaps this slice does not widen.)
- **`regclassout`'s TOAST-relation name** (`ToastRelName`, `expr.go:788-794`)
  exists only in the `::regclass` cast path, not in `appendTypedCellText`, so
  `RegOut` does not reproduce it either. Inherited SELECT-path gap, recorded.
- **Array elements of reg*** (`regclass[]`) COPY FROM still numeric-parse a name
  element (`coerceRowForConstraintChecks` skips `IsArray` columns; the array
  encoder has no per-element reg* resolver). Pre-existing, out of scope.
- The general COPY-FROM input-error SQLSTATE (22P04 wrap vs PG's per-type codes)
  is unchanged for non-reg* types — pre-existing, separately tracked.
