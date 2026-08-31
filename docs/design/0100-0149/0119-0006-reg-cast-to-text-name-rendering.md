# reg* → text/varchar/name/bpchar cast renders the name (M0119-0006, deferral row 1350)

## Status

accepted

## Problem

Casting a `reg*` value to a string type renders the **raw numeric OID**, not the
object name. Concretely (goopg HEAD, all diverge from PG 18.3):

```sql
SELECT 'pg_type'::regclass::text;            -- goopg `1247`    PG `pg_type`
SELECT 'f_varbit(varbit)'::regprocedure::text; -- goopg `131072` PG `f_varbit(bit varying)`
SELECT 'f_varbit'::regproc::text;            -- goopg `131072`  PG `f_varbit`
SELECT 'pg_type'::regclass::name;            -- goopg passes the KindInt through unchanged
```

The `::reg*` **input** half (name→OID) resolves correctly — it is the
**downstream cast to a string type** that renders the numeric datum. The bug was
filed as deferral row 1350 by the 74th slice (design `0119-0006-argtype-alias-format-type-be-port.md`),
which observed it as an adjacent discovery and left it out of scope.

## Root cause

A `reg*` datum has no distinct kind — it is a plain `KindInt` holding the object
OID. `evalCastTyped` (`internal/executor/expr.go`) delegates every cast to
`evalCast`, whose `text`/`varchar`/`bpchar`/`char` arm formats a `KindInt` as a
decimal integer (`strconv.FormatInt(d.Int, 10)`), and whose `name` arm's
`default:` returns the `KindInt` unchanged. The reg*out name resolution already
exists — `executor.RegOut` (`internal/executor/reg_identifier.go`), a literal port
of the `reg*out` family in `regproc.c` — but is wired only into the **SELECT**
(`appendTypedCellText`, `internal/server/dispatch.go`) and **COPY**
(`datumToCopyText`) paths. The `::text` cast is the missing third sibling
(`pattern_sibling_paths_must_agree`; the 68th slice collapsed SELECT+COPY onto
one `RegOut`, design `0119-0006-reg-copy-text-name-rendering.md`).

## Fix

In `evalCastTyped` — which already receives the **source** type name and the
session `*Context` (both absent from `evalCast`'s frozen signature) — add a
reg*-source guard *before* delegating to `evalCast`:

when `sourceType` ∈ {`regclass`, `regproc`, `regprocedure`, `regtype`,
`regrole`, `regcollation`} (case-insensitive; reuse `isRegIdentifierTypeName`
if it already lists exactly these six), AND `targetType` ∈ {`text`, `varchar`,
`name`, `bpchar`}, AND `d.Kind == KindInt`:

return `NewStringDatum(RegOut(sourceType, uint32(d.Int), ctxCatalog, qualify))`.

- `ctxCatalog` is `ctx.Catalog` (nil-guarded; `regOut` already degrades to the
  numeric OID when the catalog is nil or the object is dangling).
- `qualify` mirrors the SELECT path's `!publicSchemaVisible(getSetting)`
  exactly: **true** when the `public` schema is not on the session's effective
  search_path (including the pg_dump `search_path=''` case). Compute it from
  `ctx` via `searchPathSchemas(ctx)` / `RegObjectSchemaVisible(ctx, "public")`;
  do **not** introduce per-object schema qualification — that is deferral row
  1347 and stays out of scope.
- `char` (the one-byte CHAROID type, distinct from `bpchar`) is deliberately
  **excluded** from the target set: it has `charin`/`charout` semantics (first
  byte), not the plain-name semantics the other four share, and is not named in
  row 1350.

Because the planner stamps `CastExpr.SourceType` from the operand's type, this
one guard also fixes the column shape the ledger did not probe: a stored `reg*`
column cast to text (`regcol::text`) was likewise `KindInt` → raw OID, and now
renders its name. It leaves `regtype`'s name→oid input arm (which already
returns the name as a `KindString`) untouched — a `KindString` reg* value
bypasses the guard and flows through `evalCast` unchanged, preserving the
"regtype / non-literal sources unaffected" behavior row 1350 recorded.

## Scope

- `internal/executor/expr.go` — `evalCastTyped` (the guard); a package-local
  reg*-source / string-target predicate if `isRegIdentifierTypeName` does not
  already fit; a `qualify` computation mirroring the SELECT path.
- new test `internal/executor/reg_cast_to_text_test.go` (or a sibling added to
  the existing reg test files).

## Acceptance

1. The five literals above render the name, byte-identical to PG 18.3.
2. `regtype`/`regrole`/`regcollation` sources render their names too.
3. A stored `reg*` column cast to text renders its name (`regcol::text`).
4. OID 0 → `-`; a dangling OID → numeric (unchanged `RegOut` behavior).
5. Sibling agreement: the cast path and the SELECT path produce byte-identical
   reg* text for the same OID under the same search_path (a dedicated test,
   mirroring `TestRegCopyAndSelectSiblingArgQualifyAgree`).
6. `char` (CHAROID) target is unchanged (still `evalCast`'s arm).

## Gates

`go test ./internal/executor/` · `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
· `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) · `TestPort_RegressSuite`.
