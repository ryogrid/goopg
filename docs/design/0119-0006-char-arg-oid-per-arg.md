---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (char-arg OID per arg)
---

# regprocedure arglist carries the resolved arg-type OID (bare `char` vs `"char"`)

Closes deferral ledger row 1351 (the 77th slice's remaining half). The
regprocedure arglist stores only each arg's type NAME string, so a BARE `char`
arg — which goopg's parser stamps bpchar-like (`Args=[1]`), matching PG's
grammar — is indistinguishable from OID-18 `"char"`, and both render `"char"`
where PG's `format_type_be` renders `character` for the bare (bpchar) form.
The fix carries the resolved type OID for the one ambiguous spelling through the
stored arglist and lets the renderers disambiguate the `char` arm on OID,
exactly as upstream `format_procedure_extended` feeds `pg_proc.proargtypes`
OIDs to `format_type_be`.

## 1. Problem

`format_procedure_extended` (regproc.c:332) renders an input-arg type by pulling
the RESOLVED OID from `procform->proargtypes.values[i]` and feeding it to
`format_type_be`. goopg's arglist is name-string-based: `Routine.ArgTypes[i]` is
`Type{Name string; Args []int64; IsArray bool}` and the renderers
(`regprocedureArglist` executor / `formatProcedureArglist` catalog) key the
format_type_be port `ArgTypeDisplayAlias` on `Name`. The two spellings collide:

| arg | stored | PG `format_type_be` | goopg today |
|---|---|---|---|
| `char` (bare) | `Name="char", Args=[1]` | `character` (BPCHAROID 1042) | `"char"` |
| `"char"` (quoted) | `Name="char", Args=nil` | `"char"` (CHAROID 18) | `"char"` |

The parser already distinguishes them via the `Args` stamp (ddl.go:4666-4669:
`first.Kind != TokenQuotedIdent && EqualFold(char/character) → Args=[1]`; a
quoted `"char"` gets no stamp), but the renderers discard `Args` (the
`RegprocArg` projection at catalog.go:19910 carries only `{Name, Schema}`), so
the distinction is lost.

`TypeNameToOID("char")` maps unconditionally to BPCHAROID (codec.go:1640) — it
has no CHAROID(18) arm and can never reach 18, so re-deriving the OID from the
Name at render time is impossible; the OID must be captured at CREATE time where
the `Args` stamp is still available.

## 2. The upstream rule

- `format_procedure_extended` (regproc.c:332): `thisargtype =
  procform->proargtypes.values[i]` then `format_type_be(thisargtype)`
  (regproc.c:377-380), with typmod −1 (regproc.c:361-368) so the arglist never
  emits `character(1)`.
- `format_type_be` (format_type.c:343) = `format_type_extended(oid,-1,0)`:
  BPCHAROID hits the switch case at :207 → `"character"`. CHAROID has NO switch
  case, so it falls to the default path (:307-324):
  `quote_qualified_identifier(get_namespace_name(typnamespace), typname)` → the
  `char` keyword is quote_identifier'd → `"char"`. A true array appends `[]`
  after the (possibly quoted) element (:328-329).
- The grammar (`gram.y:14755` `CharacterWithoutLength`) stamps a BARE
  `character`/`char` with typmod `[1]` → bpchar(1042); a quoted `"char"` resolves
  through `typenameTypeIdAndMod` (parse_type.c) to the single-byte CHAROID(18).
  goopg's parser mirrors the stamp exactly.

The OID is a **type property**, fixed at CREATE FUNCTION/PROCEDURE when the arg
type name resolves. Only the single spelling `char` is ambiguous (bare bpchar vs
quoted CHAROID): every other spelling's name is unambiguous, and its
name-based `ArgTypeDisplayAlias` is already a faithful `format_type_be` port.
So the OID need only be carried for `char`, and the renderers act on it only
there.

## 3. goopg implementation

Four parts (mirror the 73rd slice's `ArgTypeSchemas` threading, whose shape is
proven and backward-compatible).

### 3.1 Store the resolved OID per arg (char family only)

New `Routine.ArgTypeOIDs []uint32`, parallel to `ArgTypes`/`ArgTypeSchemas`
(same length/order), populated in `execCreateFunction` (operators_ddl.go:11866)
and `execCreateProcedure` (:12610) in the same loop that builds `ArgTypes` —
alongside `argTypeSchemas[i] = argTypeSchema(a.Type)`:

```go
// argTypeOID returns a NON-ZERO pg_type OID only for the one ambiguous arg-type
// spelling, `char`: a BARE char (bpchar, the gram.y CharacterWithoutLength
// stamp gives Args=[1]) is OIDBpChar(1042), a quoted "char" (CHAROID, no stamp,
// Args nil) is OIDChar(18) — deferral row 1351. Every other spelling returns 0:
// its name-based ArgTypeDisplayAlias is already a faithful format_type_be port,
// so carrying a name-derived OID would only add wrong array-element OIDs and
// risk a proargtypes shift. Key on the RAW element name (a.Type.Name, BEFORE
// routineArgTypeName bakes the `[]` suffix), so `char[]` and `"char"[]` land
// here too.
func argTypeOID(a parser.ColumnType) uint32 {
    if a.Type.Name == "char" {
        if len(a.Args) == 0 { return catalog.OIDChar } // quoted "char" / "char"[]
        return catalog.OIDBpChar                        // bare char / char[] (Args=[1])
    }
    return 0
}
```

The `char` case is the ONLY one the renderers act on; every other arg's OID is 0
and renders through the existing name alias (unchanged). `character`/`bpchar`
spellings stay 0 — their NAME is unambiguous (always bpchar → `character`) and
the alias already renders them; user-defined types stay 0 (their real OID needs
the §6 store-namespace gap).

### 3.2 Expose the OID to the renderers

`RegprocArg` (catalog.go:19910) gains `OID uint32`. `RegprocedureNameParts`
(catalog.go:19926):
- builtin arm → `OID: 0` (pg_proc.dat's `"char"` always means OID 18,
  unambiguous; the renderer's `char` arm handles OID 18 and OID 0 identically —
  see §3.3).
- user arm → `RegprocArg{Name: t.Name, Schema: ArgTypeSchemas[i], OID:
  ArgTypeOIDs[i]}` (nil `ArgTypeOIDs` on a pre-change reloaded routine → 0).

### 3.3 Disambiguate the `char` arm in BOTH sibling renderers

In the executor `regprocedureArglist` (reg_identifier.go:660) and the catalog
`formatProcedureArglist`/`argListTypeDisplay` (catalog.go:19967/19980), the
`char` element name currently yields `ArgTypeDisplayAlias("char") == "char"`.
Add, at the point the element base name is computed:

```go
if base == "char" && a.OID == catalog.OIDBpChar { // bare char → bpchar
    base = "character"
}
// then ArgTypeDisplayAlias / quoteQualifiedIdentifier as today
```

- `a.OID == OIDBpChar` (1042) → `character` (then `[]` re-appended for arrays).
- `a.OID == OIDChar` (18) or `a.OID == 0` (builtin, or pre-change data dir) →
  unchanged `"char"` via `ArgTypeDisplayAlias` — exactly today's behavior, so
  backward compat is free and the builtin path (always OID 18) is correct.

The catalog sibling `argListTypeDisplay` (catalog.go:19981) currently takes only
`name string`; change its signature to `argListTypeDisplay(name string, oid
uint32)` and pass `a.OID` from `formatProcedureArglist` (:19968). The array
suffix split (`mytype[]` → element + `[]`) already happens before aliasing in
both renderers; the `char` check rides on the ELEMENT name, so `char[]` →
`character[]` and `"char"[]` → `"char"[]`.

### 3.4 Serialization and backward compat

`ArgTypeOIDs []uint32` (`json:",omitempty"`) rides the whole-Routine JSON
round-trip (`pgProcArgMetaJSON` sys_pg_proc.go:218 / `DecodePGProcArgMeta`
:240), the exact precedent `ArgTypeSchemas` used. A pre-change data dir reloads
with `ArgTypeOIDs` absent → nil → OID 0 → the renderer's OID-0 arm reproduces
today's output verbatim. No catalog version bump; no migration.

### 3.5 proargtypes (the ledger's literal "proargtypes OID per arg")

`buildPGProcRow` (sys_pg_proc.go:144-147) currently derives `argOIDs[i] =
catalog.TypeNameToOID(t.Name)`, which is wrong for a quoted `"char"` (always
1042, should be 18). Switch it to prefer the stored OID:

```go
if i < len(r.ArgTypeOIDs) && r.ArgTypeOIDs[i] != 0 {
    argOIDs[i] = r.ArgTypeOIDs[i]
} else {
    argOIDs[i] = catalog.TypeNameToOID(t.Name)
}
```

Because `ArgTypeOIDs` is char-only non-zero, this override fires ONLY for a
`char` arg: bare `char` → 1042 (equals today's `TypeNameToOID("char")`, no
change) and quoted `"char"` → 18 (the fix). Non-char args — including arrays
and user types — keep today's `TypeNameToOID` derivation, so there is no
proargtypes shift for arrays (review finding 4). `buildPGProcRow` runs only for
user-created routines (via `walLogCreateRoutine` → `syncRoutineToCatalogHeap`,
its sole caller), all of which populate `ArgTypeOIDs` at capture; the
`i < len(r.ArgTypeOIDs)` guard is OOB-safe for any path that predates the field.

## 4. Gates

New/extended unit tests (expected strings pinned to §1; array cases pinned per
§3.3):

- Extend `TestRegprocedureArglistCatalogAndExecutorAgree`
  (reg_qualify_test.go:460) with `{Name:"char", OID:OIDBpChar}` → both siblings
  render `character`, `{Name:"char", OID:OIDChar}` / `{Name:"char", OID:0}` →
  `"char"`, and the array forms `{Name:"char[]", OID:OIDBpChar}` →
  `character[]` / `{Name:"char[]", OID:OIDChar}` → `"char"[]` (sibling pin —
  Hard-won Rule #2).
- `TestCreateFunctionCapturesCharArgOID` — a live `CREATE FUNCTION f(char)` and
  `CREATE FUNCTION g("char")` store `ArgTypeOIDs = [1042]` and `[18]`
  respectively (and the array forms `f(char[])` → `[1042]`, `g("char"[])` →
  `[18]`), and `oid::regprocedure` renders `f(character)` / `g("char")` /
  `f(character[])` / `g("char"[])`.
- `TestRegprocedureCharArgCastAndWireAgree` — the `::regprocedure` cast sibling
  and the SELECT/COPY wire path agree on `character` vs `"char"` for the same
  two routines (the §3 sibling-paths audit).
- `regproc_name_test.go` (constructed `ArgTypes` with Args nil, no OID) stays
  green via the OID-0 fallback.

Gates: package suites (`internal/executor`, `internal/server`,
`internal/catalog`), pre-commit units, `TestPort_RegressSuite`,
`scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).

## 5. Files

- `internal/catalog/routines.go` — `Routine.ArgTypeOIDs []uint32`
- `internal/catalog/catalog.go` — `RegprocArg.OID uint32`; user arm of
  `RegprocedureNameParts` populates it; `char` arm in
  `formatProcedureArglist`; `argListTypeDisplay(name, oid)` signature
- `internal/executor/operators_ddl.go` — `argTypeOID` helper + populate
  `ArgTypeOIDs` in execCreateFunction/execCreateProcedure
- `internal/executor/sys_pg_proc.go` — `buildPGProcRow` prefers stored
  `ArgTypeOIDs` (char-only, guarded)
- `internal/executor/reg_identifier.go` — `char` arm in `regprocedureArglist`
- tests: reg_qualify_test.go, regproc_name_test.go, operators_ddl capture test

## 6. Deferred (new ledger rows) and notes

- **The pg_get_function_* introspection family renders a `char` arg
  unconditionally as `character`.** `canonicalTypeName` (expr.go:15362
  `case "char": return "character"`) backs `pg_get_function_arguments` /
  `pg_get_function_identity_arguments` / `pg_get_functiondef` /
  `pg_get_function_result` (expr.go:15045/15078/15155/15176/10282), which pg_dump
  uses to rebuild CREATE FUNCTION signatures. It is name-keyed (no OID), so a
  quoted `"char"` (CHAROID 18) renders `character` where PG's `format_type_be`
  renders `"char"` — a pre-existing divergence, a SEPARATE renderer family from
  the regprocedure output path (row 1351 names only the regprocedure arglist),
  **RESOLVED 2026-08-15 (92nd slice):** `canonicalTypeName` re-signed to
  `canonicalTypeName(name string, oid uint32)`; its `char` arm emits `"char"`
  for OIDChar(18) and `character` for 1042/0 (0 keeps the no-OID baseline for
  aggregates + pre-90th routines). All 7 call sites updated: the three per-arg
  loops (`buildFunctionArguments`/`buildTableResult`/`buildFunctionDef` arg-arms)
  read `r.ArgTypeOIDs[i]` under a nil/len guard; `buildFunctionDef` RETURNS-clause,
  `pg_get_function_result` scalar, and `routineArgListStr` pass 0. Test
  `TestPgGetFunctionArgsQuotedCharRendersQuoted`. Residuals: `RETURNS "char"`
  was OID-less (no `ReturnTypeOID` on `Routine`) — **RESOLVED 2026-08-15 (94th
  slice):** `ReturnTypeOID uint32` added; `argTypeOID`'s body extracted as shared
  `charTypeOID` and reused for the RETURN path; `pg_get_function_result` /
  `buildFunctionDef` RETURNS-clause / prorettype all thread it (row 1361).
  Remaining: the arg-list parser rejects a named `g(x "char")` (row 1362).
  ARRAY-typed args/returns write the ELEMENT OID into proargtypes/prorettype
  where PG writes the ARRAY OID — **RESOLVED 2026-08-15 (row 1364, see §7).**
- **Quoted `"char"(N)` with an explicit typmod** (`Args=[N]`) is
  indistinguishable from bare `char(N)` by the `len(Args)` heuristic →
  misclassified as bpchar. The realistic oracle probes (`"char"`, `"char"[]`,
  `char`, `char[]`) are covered; a faithful fix needs a `Quoted` flag on
  `parser.ColumnType` (parser change), recorded rather than widened here.
- **User-defined arg types still resolve to OIDText(25)** in proargtypes —
  goopg's user-type store has no namespace field (the 73rd slice §6 gap); this
  slice's `ArgTypeOIDs` carries only the builtin `char` ambiguity and does not
  touch user-type OID resolution.
- The bare builtin-array alias gap in the catalog `formatProcedureArglist`
  (§6 of the 73rd design doc) is unchanged; this slice touches only the `char`
  element.

## 7. Array half of the scalar slice (deferral row 1364)

The array arms of the `char` capture + the `TypeNameToOID` name fallback:

1. **Capture-time** — `charTypeOID` (operators_ddl.go) intercepts `IsArray`
   FIRST: quoted `"char"[]` (Args nil) → `OIDArrayChar`(1002), bare `char[]`
   (Args=[1]) → `OIDArrayBpChar`(1014), any non-char array → 0. Both
   `ArgTypeOIDs[i]` and `ReturnTypeOID` inherit it (the scalar rows 1351/1361
   arms).
2. **Render/fallback-time** — `TypeNameToOID` (codec.go) gained the `[]` array
   block ported VERBATIM from `initdb/pg_proc_view.go:typeNameToOIDStr` (the
   view builder was already array-correct), so `buildPGProcRow`/index keys
   resolve baked names like `int4[]`→1007 and `date[]`→1182 instead of falling
   to OIDText(25). Both char spellings map to 1002 (never 1014 via the scalar
   arm). The three renderer siblings that disambiguate the `char` element on OID
   (`regprocedureArglist`, `argListTypeDisplay`, `canonicalTypeName`) accept the
   ARRAY OIDs alongside the scalar ones so `"char"[]` keeps its quotes and bare
   `char[]` renders `character[]`. Non-char arrays stay 0 at capture and resolve
   via the fixed fallback; user-type elements still fall back to OIDText (the
   §6 "Q2" user-type gap, separate ledger item).
