# M0134-0168 — `'name'::regclass` must BE `regclassin`

**Status:** landed 2026-08-29
**Milestone task:** M0134-0168 (`postgres/src/test/regress/sql/sqljson.sql`)
**Upstream reference:** `postgres/src/backend/utils/adt/regproc.c:960-1000`
(`regclassin`), `:1793-1820` (`stringToQualifiedNameList`), `:1865-1882`
(`parseDashOrOid`), `postgres/src/backend/catalog/namespace.c:3556-3580`
(`makeRangeVarFromNameList`), `:440-470` (`RangeVarGetRelidExtended`)

## Why this doc exists

`sqljson.sql` was sized live for the first time this loop. Its divergence is
almost entirely one REFACTOR-tier missing subsystem (see "What was parked"), so
the shipped change is an **engine-wide** correctness fix the case merely
exposed — the same shape as M0134-0163/-0164/-0165/-0166/-0167.

## The bug

`evalExprSlot`'s `*optimizer.CastExpr` arm (`internal/executor/expr.go`) carried
its own inline implementation of the string-input `::regclass` cast: parse the
name, look it up in `Catalog.LookupTable`, then in `InMemory.LookupIndex` — and
on a **miss, fall through**. Falling through returns the *original operand*, so
`'nosuch'::regclass` evaluated to the `KindString` `"nosuch"`: a regclass value
naming a relation that does not exist. Nothing anywhere rejected it.

Three consequences, all silent:

1. **No error where PG raises one.** PG's `regclassin` calls
   `RangeVarGetRelid(rel, NoLock, missing_ok=false)` and raises
   `relation "nosuch" does not exist` (42P01) at the cast. goopg returned a
   value.
2. **The wrong error, later, somewhere else.** The bogus regclass is a string,
   so the *next* cast in a chain misparses it. psql's `\sv` issues
   `SELECT '<name>'::pg_catalog.regclass::pg_catalog.oid`; goopg answered
   `invalid input syntax for type oid: "<name>"` (22P02) — an error about the
   OID input function, naming a relation, for a query that never mentions an
   OID literal. That is the form the divergence takes in `sqljson.sql`
   (six occurrences).
3. **Silently empty result sets.** A resolved regclass compares as an OID; an
   unresolved one compares as text. Any `... WHERE confrelid = '<name>'::regclass`
   matched nothing. psql's `\d` "Referenced by:" section is exactly this shape,
   which is why it was **missing entirely** from goopg's `\d` output for every
   FK-referenced table.

The miss path was not the only gap. The inline arm also lacked
`parseDashOrOid`, so `'-'::regclass` and `'12345'::regclass` stayed strings
rather than becoming `InvalidOid` / a numeric OID, and it split the name with
`splitRegQualifiedName`, which has no segment-count rule, so
`'a.b.c.d'::regclass` and `'somedb.public.t'::regclass` were simply lookup
misses.

## Why it survived: a sibling that already had the right answer

goopg already owned a faithful `reg*in` port —
`regIdentifierInput` (`internal/executor/reg_identifier.go`), written by
M0119-0006's 67th/72nd slices. It handles `parseDashOrOid`, the
`SplitIdentifierString` port, and every reg\* type's undefined-object error,
**including regclass's 42P01**. But its callers were only the heap-write path
(`coerceRowForConstraintChecks`, `operators_storage.go`), the `reg*[]` array
element path (`codec_array.go`) and `CoerceParamToDeclaredType` (EXECUTE
parameters, M0134-0005a). The SQL-visible scalar cast — by far the most-used
entry point — went to the duplicate.

`CoerceParamToDeclaredType`'s own doc comment records the trap that kept the two
apart: M0134-0005a tried widening the low-level `evalCast` `case "regclass"`
and regressed `TestRegCastToStringRendersName`, so it routed *its* callers to
`regIdentifierInput` directly and left the CastExpr arm alone. That is the
right seam — it is the `evalExprSlot` CastExpr arm, not `evalCast`, that
implements the SQL-level cast — and this change takes it.

This is the recurring shape from `pattern_sibling_paths_must_agree` and
Hard-won Rule #2: **encode/decode, fast-path/interpreted, and here
input-primitive/inline-duplicate.** A green test on one twin proved nothing
about the other.

## What landed

1. `internal/executor/expr.go` — the CastExpr `regclass` `KindString` arm is
   now a single `return regIdentifierInput(v, "regclass", ctx, x.Pos())`. The
   `KindInt` arm (OID → name, `regclassout`) is untouched.
2. `internal/executor/reg_identifier.go` — the shared `regclass` arm gained
   `makeRangeVarFromNameList`'s segment-count rules, so *both* callers get
   them:
   - `> 3` segments → 42601 `improper relation name (too many dotted names): %s`
     (namespace.c:3576);
   - exactly 3 segments → the leading segment is a **catalog** name; it must
     equal the current database or `RangeVarGetRelidExtended` raises 0A000
     `cross-database references are not implemented: "%s"` (namespace.c:455-462).
     When it matches, it is dropped and `schema.relation` resolves normally.
   Only regclass reaches `makeRangeVarFromNameList` upstream, so the rule lives
   in that arm rather than in `splitRegQualifiedName`.

## Verification

`internal/executor/regclass_input_test.go` pins eleven shapes against
byte-exact PG 18.3 answers captured live from the oracle under `./postgres/`
(hits → OID, index names, `-`/digit passthrough, and the five error classes).
Revert-checked: **8 sub-assertions fail** with the two source files stashed.

15-case regress A/B against a HEAD worktree: **10 byte-identical**, and of the
five that moved,

| case | base → new | verdict |
|---|---|---|
| `sqljson` | 1771 → 1771 | 6 nonsense 22P02 lines became PG's real 42P01 text |
| `create_index` | 3340 → **3335** | `\d` "Referenced by:" now matches PG |
| `alter_table` | 3754 → **3728** | same |
| `inherit` | 3251 → **3238** | same |
| `foreign_key` | 3486 → **3490** | see below |

`foreign_key` is a net-forward +4: the `Referenced by:` section went from
*absent* (PG's lines showed as `-`) to *present and matching*, which unmasked a
**pre-existing, unrelated** describe bug — goopg lists a partitioned table's
per-partition FK constraints alongside the parent's, where PG lists only the
parent's. Those rows belong to output goopg had never emitted before. Ledgered
as 0168b; the net across the five moved cases is −36 lines.

## What was parked (the case itself)

`sqljson.sql` stays `failed`. Its 1771-line diff is one REFACTOR-tier missing
subsystem: goopg's grammar has **no SQL/JSON constructor or predicate support
at all** — `JSON()`, `JSON_SCALAR()`, `JSON_SERIALIZE()`, `JSON_OBJECT()`,
`JSON_ARRAY()`, `JSON_OBJECTAGG()`, `JSON_ARRAYAGG()` and `IS [NOT] JSON` — nor
their clause vocabulary (`FORMAT JSON [ENCODING …]`, `RETURNING <type>`,
`{WITH|WITHOUT} UNIQUE KEYS`, `{NULL|ABSENT} ON NULL`, a query-expression
argument). Every one of the 206 `^+ERROR` and all 53 `^-ERROR` lines belongs to
that single bucket; there is no second root cause to slice off. Upstream is
`postgres/src/backend/parser/gram.y` (`json_value_expr`, `json_object_constructor`,
…), `parse_expr.c` (`transformJsonValueExpr` and friends) and
`src/backend/utils/adt/jsonfuncs.c`. See ledger row 0168a — it is its own
milestone, and it also gates M0134-0169 (`sqljson_jsontable.sql`) and -0170
(`sqljson_queryfuncs.sql`).

## Known remaining divergences in the shipped area

- **Caret position.** goopg reports the reg\* input error at the *cast*
  position; PG reports it at the string literal (`'nosuch'::regclass` — PG's
  `^` sits under the quote, goopg's under the `::`). Family-wide, pre-existing,
  and not introduced here. Ledgered 0168c.
- **The other reg\* types still fall through.** `'nosuch'::regtype`,
  `'nosuch'::regrole` and `'nosuch'::regnamespace` still return the raw name —
  the identical silent-acceptance bug, on arms whose CastExpr code carries
  extra semantics (regtype's oidvector and numeric-string rendering) that must
  be preserved across the same delegation. `regIdentifierInput` already has
  regtype/regrole arms; regnamespace has no name-resolution seam at all (a
  pre-existing recorded gap). Ledgered 0168d.
- **`pg_get_viewdef('nosuch'::regclass)`** still returns empty rather than
  raising, so some compat intercepts consume the argument without evaluating
  the cast. Ledgered 0168e.
- **`to_regclass`/`to_regtype`** and the rest of the `to_reg*` family do not
  exist. Ledgered 0168f.
