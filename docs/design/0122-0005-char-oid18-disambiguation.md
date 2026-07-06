# 1-byte `"char"` (OID 18) disambiguation (M0122-0005)

Status: accepted
Date: 2026-07-05
Supersedes: none

## Problem

PostgreSQL has two distinct fixed-length character types that share the
surface spelling `char`:

- **`bpchar`** (pg_type OID 1042) — blank-padded `character(n)`. The bare
  `CHAR`/`CHARACTER` SQL keyword is grammar-mapped directly to this type with
  an implicit length of 1 when none is written (`postgres/src/backend/parser/
  gram.y`'s `Character` production always emits `{"pg_catalog", "bpchar"}`).
- **`"char"`** (pg_type OID 18) — an internal 1-byte type used throughout
  system catalogs (`relkind`, `typtype`, …). Its `pg_type.typname` is
  literally `char`, so it is reached only via ordinary type-name lookup on
  the *quoted* identifier `"char"` — never via the reserved keyword.

`unimplemented_feat.json`'s DU-002 entry recorded the gap plainly: "`\"char\"`
(18) is deferred: the parser folds both quoted `\"char\"` and bare char to
`ct.Name=\"char\"`, so disambiguating the 1-byte internal char from bpchar
needs a parser change, not just a codec entry."

`internal/initdb`'s catalog seed for OID 18 was already byte-correct
(`Name: "char", Len: 1, ByVal: true, Category: 'Z'`, matching
`postgres/src/include/catalog/pg_type.dat`), and the CREATE-TABLE
column-declaration path already had a partial fix
(`internal/parser/ddl.go:4497` + `internal/executor/
pg18_user_catalog_rows.go:616-626`: bare `char` synthesizes an implicit
typmod of 1, and a post-hoc override corrects `atttypid` to OID 18 when the
column type is `"char"` with zero typmod args). What remained open, and is
the actual subject of this change, is the **expression-cast path**
(`x::char`, `x::"char"`, `CAST(x AS char)`) and the **wire-protocol
TypeOID**/`pg_typeof()` consumers downstream of it — none of which had ever
been extended to carry the same disambiguating signal.

## Design

The parser discards *how* an identifier was written (quoted vs. bare
keyword) as soon as it builds the type name string — by the time a
`CastExpr` exists, `Type.Name == "char"` either way. Typmod presence is the
only signal available to carry the distinction forward, exactly as the
existing column-declaration fix already established. This change extends
that same signal to the cast-expression path:

1. **Parser** (`internal/parser/select.go`): a new `synthesizeBareCharTypmod`
   helper, called from both `parseCastTail` (`::` casts) and
   `parseCastFuncExpr` (`CAST(...AS...)`), inspects the type-name token's
   kind (captured via `p.cur()` immediately before `parseTypeNameAfterCast`
   is invoked) and synthesizes `Typmods = []int64{1}` when the token was
   *not* `TokenQuotedIdent`, the name is `"char"`, and no explicit typmod was
   already written. Quoted `"char"` is left with empty `Typmods`.

2. **Planner** (`internal/planner/planner.go`'s `exprType()`): the
   `*CastExpr` case now populates `catalog.Type.Args` from `x.Typmod`,
   narrowly scoped to `TargetType == "char"` (every other cast target's
   `exprType()` result is byte-for-byte unchanged, deliberately avoiding any
   wider blast radius). Empty `Args` therefore means "this came from a
   quoted `\"char\"`"; non-empty means "bare `char`/`char(N)`, i.e. bpchar."

3. **Wire protocol** (`internal/server/dispatch.go`'s `typeOIDFor`):
   signature changed from `(name string) uint32` to `(t catalog.Type)
   uint32` (4 call sites: `dispatch.go` ×2, `dispatch_extended.go`,
   `extended.go`) so the `RowDescription`/cursor-`FieldDescription` TypeOID
   can consult `Args` the same way. `case "char"` now returns
   `catalog.OIDChar` (18) when `len(t.Args) == 0`, else falls through to
   bpchar's 1042 — this also fixes real **table columns** declared `"char"`
   (not just cast expressions), since `typeOIDFor` is called uniformly for
   every projected `SchemaColumn`, including plain `ColumnRef`s whose
   `catalog.Type` already carries the DDL-side `Args` signal.

4. **`pg_typeof()` display** (`internal/planner/planner.go`'s
   `pgTypeofDisplayName`, 3 call sites): signature changed from `(name
   string) string` to `(t catalog.Type) string`. `case "char"` now returns
   the literal string `` `"char"` `` (with embedded quotes) when `Args` is
   empty, matching PostgreSQL's `format_type_be`, which quotes a type name
   that collides with a reserved keyword; otherwise returns `"character"` as
   before.

`internal/executor/expr.go`'s `pgTypeofNameFromPlanType` is a byte-for-byte
duplicate of `pgTypeofDisplayName` with the identical un-split `"char"`
case, but has **zero callers anywhere in the repository** (confirmed via
`find_referencing_symbols` and a repo-wide grep) — left untouched as dead
code rather than a missed sibling-path update.

## Verification

Built goopg and a real PostgreSQL 18.3 instance side by side (ports 5533 and
5545, both from a fresh `initdb`) and compared:

```sql
CREATE TABLE chartest (a "char", b char, c char(3), d bpchar);
SELECT a, b, c, d FROM chartest \gdesc
\d chartest
SELECT pg_typeof('x'::"char"), pg_typeof('x'::char), pg_typeof('x'::char(3));
```

Column `a` (`"char"`) reports TypeOID for OID 18 and displays as `"char"` in
both `\gdesc` and `\d` on goopg, matching real PG exactly; `pg_typeof`
returns `"char"` for the quoted form and `character`/`character(3)` for the
bare forms on both servers.

Gates: `go build ./...` clean; `go test ./internal/parser/...
./internal/planner/... ./internal/server/... ./internal/executor/...
./internal/catalog/...` all PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2,
Q13=33).

## Tests

- `internal/parser/cast_test.go`: `TestParseCastCharDisambiguation` (5
  subcases — bare/explicit-length/quoted, both `::` and `CAST()` forms).
- `internal/planner/pg_typeof_test.go`: `TestPgTypeofCharDisambiguation`.
- `internal/server/typeoid_char_test.go`: `TestTypeOIDForCharDisambiguation`.

## Deferred

Two residual gaps surfaced during verification, both pre-existing and
confirmed unaffected (neither introduced nor worsened) by this change — see
`.ralph/deferral_ledger.md`'s 2026-07-05 M0122-0005 row for full detail:

1. ~~**Value-truncation semantics**~~ — **closed 2026-07-06**, see "Follow-up:
   inline-cast value truncation" below.
2. **`pg_typeof(...)::oid` fails for every type**, not just `"char"` (e.g.
   `pg_typeof(1)::oid` also errors) — `pg_typeof()`'s folded result is a
   plain display-string `StringConst`, not an OID-backed `regtype` datum.
   Still open; independently scoped, orthogonal to the OID-18
   disambiguation this change lands.

## Follow-up: inline-cast value truncation (2026-07-06)

Closes deferred item (1) above. Real PostgreSQL's `charin()`
(`postgres/src/backend/utils/adt/char.c`) takes the first byte of any input
that is not exactly a `\NNN` octal escape and silently discards the rest.
`internal/executor/expr.go`'s `evalCast` "char" branch only handled the
octal-escape form; a plain multi-byte string (`'xyz'::"char"`) passed
through unchanged instead of truncating to `'x'`.

Fixed by extending that same branch: after the octal-escape check fails,
take the first byte of the input (or `0` for an empty string) and render it
through the existing `charTypeDisplayForm` helper — identical rendering
rules PostgreSQL's `charout()` uses (empty string for byte 0, plain ASCII
for printable bytes, `\NNN` octal for the rest).

The tricky part is that `evalCast`'s "char" branch must NOT fire for the
bare `char`/CHARACTER keyword form — grammar-synthesized to the *same*
`TargetType=="char"` string with `Typmod==1` (a distinct bpchar(1) cast;
see the base design above), whose own typmod-truncation/padding semantics
are a separate, broader, still-unimplemented gap. Since `evalCast`'s shared
signature takes only a type-name string (no typmod), the disambiguation
happens at the one call site that has `Typmod` in scope
(`internal/executor/expr.go`'s `*planner.CastExpr` case in `evalExpr`,
mirroring `exprType()`'s own `x.Typmod` check in the base design): when
`x.TargetType == "char" && x.Typmod > 0` the call renames the target type to
`"bpchar"` for that one `evalCastTyped` invocation only (still matches the
shared `"text","varchar","bpchar","char"` switch case, but skips the
inner OID-18-specific branch) — leaving genuine OID-18 casts (`Typmod == 0`)
untouched.

Tests: `internal/executor/char_oid18_truncation_test.go`'s
`TestEvalCastCharTruncatesToFirstByte` (direct `evalCast` unit coverage,
including the octal-escape precedence case) and
`TestCastExprCharTypmodDisambiguation` (full parse→plan→eval pipeline,
pinning that `SELECT 'xyz'::"char"` truncates to `"x"` while
`SELECT 'xyz'::char` stays unchanged at `"xyz"`).

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/parser/... ./internal/server/...
./internal/catalog/...` all PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2,
Q13=33).

## Follow-up: varchar(n)/bpchar(n)/char(n) inline-cast truncation (2026-07-06)

Closes the "bpchar's own typmod truncation/padding" gap called out in the
previous follow-up above — `'xyzzy'::varchar(3)` and `'xyzzy'::char(3)`
previously passed through the `evalCast` "text/varchar/bpchar/char" branch
unchanged, with no length enforcement at all.

Verified against real, unmodified PostgreSQL 18.3 (`psql`/`initdb` under
`postgres/local_install`): an explicit `::type(n)` cast truncates a
too-long value **silently** (no `22001` error) — that error code is
assignment/INSERT-coercion-only, already enforced separately by
`internal/executor/codec.go`'s `coerceTextLikeDatum` (the column-storage
path). Real PG's `bpchar`/`char` additionally right-pads short values with
spaces up to `n` (verified via `octet_length`), which `varchar` does not do.

Fixed in `internal/executor/expr.go`'s `*planner.CastExpr` case in
`evalExpr` (same call site as the OID-18 fix above, since `x.Typmod` is only
in scope there): after `evalCastTyped` returns, if `x.Typmod > 0` and the
result is `KindString`, `castTargetType` (not `x.TargetType`, so the
bare-`char`→`"bpchar"` rename from the OID-18 fix is truncated too) is
matched against `varchar`/`bpchar`/`char`/`character` and the value is
truncated to `x.Typmod` runes (rune-based, matching the rune-aware
`length()`/`substr()` convention used elsewhere in `expr.go`, rather than
`coerceTextLikeDatum`'s byte-based convention). No error is raised, matching
explicit-cast semantics. This closed the disambiguation test's stale
pinned behavior too: `SELECT 'xyz'::char` (bare, `Typmod==1`) now correctly
returns `"x"` via this bpchar(1) truncation path, not unchanged `"xyz"`.

**Deferred (not implemented):** bpchar/char right-padding of short values.
goopg's `Datum` has no representation distinct from plain `KindString` for
"padded fixed-width" values, and the column-storage path
(`coerceTextLikeDatum`) already stores bpchar values trimmed rather than
padded — implementing padding only in the inline-cast path (and not in
storage) would make the two paths disagree on what a `bpchar(n)` value
looks like, which is worse than the current shared no-padding convention.
Padding both paths consistently is a separate, broader piece of work,
recorded in the ledger.

Tests: `internal/executor/char_oid18_truncation_test.go`'s
`TestInlineCastVarcharBpcharTypmodTruncation` (varchar/char/character,
exact-fit, shorter-than-n, and empty-string cases), plus the updated
`TestCastExprCharTypmodDisambiguation` expectation above.

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/parser/...` all PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=33); `make ralph-state-guard`
OK (after auto-repair of a stale status/progress marker unrelated to this
change).

## Follow-up: `pg_typeof(...)::oid` cast (2026-07-06)

Closes deferred item (2) from the original 2026-07-05 row: `pg_typeof(1)::oid`
(and every other type, not just `"char"`) raised `invalid input syntax for
type oid: "..."` instead of returning the argument's real pg_type OID.

**Root cause:** PostgreSQL declares `pg_typeof()`'s SQL return type as
`regtype`, and `regtype`'s C-level representation *is* an `Oid` — real PG's
`pg_typeof(x)::oid` is a binary-coercible relabeling cast (no parsing
involved at all), exactly like the pre-existing `regclass`/`regproc` values
this codebase already represents as a `KindInt` OID internally (only
rendered to a name string at wire-output time — see the `regclass`/`regproc`
cases in `internal/server/dispatch.go`'s `appendTypedCellText`).
`internal/executor/expr.go`'s `"pg_typeof"` case instead returned a
`KindString` holding the *display text* (e.g. `"integer"`), so a further
`::oid` cast fell through to the generic `"oid"` cast branch and tried (and
failed) to `strconv.ParseInt` the display name.

**Fix:** made `pg_typeof()` evaluate to a `KindInt` Datum holding the
resolved type's real OID, mirroring `regclass`/`regproc`'s existing
representation, so `::oid` becomes the same identity pass-through those
already get from the generic `"oid"` cast's `KindInt` branch — no change to
that branch was needed.

- New `internal/executor/expr.go` helper `pgTypeofOIDForName(cat, name)`
  resolves a display name (as produced by `planner.pgTypeofDisplayName`'s
  plan-time fold, or this same file's own Kind→name runtime fallback) to its
  OID: the quoted `"char"` special case (OID 18), the `"unknown"`
  pseudo-type (`UNKNOWNOID` = 705, per
  `postgres/src/include/catalog/pg_type_d.h` — verified, not guessed), every
  built-in name via the pre-existing `catalog.TypeNameToOID`, and
  user-defined enum/domain/composite/range/multirange types via the
  pre-existing `userTypeOIDForName` catalog lookup (the same one the
  `::regtype` string→OID cast direction already uses).
- Both the fast path (planner already folded the arg to a `StringConst`
  holding the display name) and the runtime fallback path (Kind→name
  mapping, used when the planner couldn't fold statically) now route through
  `pgTypeofOIDForName` instead of returning the name directly.
- New exported `internal/executor/expr.go` helper `RegtypeName(cat, oid)` is
  the reverse (OID→display name) — built-ins via the pre-existing
  `oidToBuiltinTypeName`, user types via `userTypeNameForOID`, `0`→`"-"`,
  `705`→`"unknown"`. Mirrors `catalog.RegprocName`/`RegprocedureName`'s role
  for the `regproc`/`regprocedure` wire-rendering cases.
- `internal/planner/planner.go`'s `exprType`'s `*FuncCall` case gained a
  `"pg_typeof"` branch returning `catalog.Type{Name: "regtype"}` (previously
  fell through to the unknown/text default) — this is what feeds the
  correct wire `TypeOID` and rendering function to a plain
  `SELECT pg_typeof(...)` with no cast at all.
- `internal/server/dispatch.go` gained a `"regtype"` case in both
  `typeOIDFor` (reports `catalog.OIDRegtype` = 2206) and
  `appendTypedCellText` (renders the `KindInt` OID back to its display name
  via the new `executor.RegtypeName`), mirroring the pre-existing
  `regclass`/`regproc` cases in the same two functions.

Verified against a real running PostgreSQL 18.3 instance side-by-side (ports
5545 goopg / 5546 real PG): `pg_typeof(1)::oid`,
`pg_typeof(1.5)::oid`→1700, `pg_typeof('x'::text)::oid`→25,
`pg_typeof(true)::oid`→16, `pg_typeof(NULL)::oid`→705,
`pg_typeof(1::"char")::oid`→18, and `pg_typeof(count(*))::oid` (the
M0097-0035 aggregate-fold path) all now resolve to the correct OID instead of
erroring; a plain `SELECT pg_typeof(...)` (no cast) still displays the exact
same text as before, and `\gdesc` now correctly reports the column's static
type as `regtype`.

**Newly observed, pre-existing, out of scope:** `userTypeNameForOID` (used
by both this fix's `RegtypeName` and the pre-existing `::regtype` int→string
CastExpr branch) unconditionally prefixes user-defined type names with
`"public."`, diverging from real PG's `regtypeout`, which only schema-qualifies
when the type isn't visible under the current `search_path` (mirroring the
more careful `regObjectSchemaVisible` check already used by the
`regproc`/`regoperator` paths). Reproduced identically via the pre-existing,
untouched `'mood'::regtype` cast — not introduced or worsened by this fix,
just newly exercised by a second call site. Recorded in the ledger, not
fixed here (broader, cross-cutting schema-visibility change affecting every
`userTypeNameForOID` caller, not a `pg_typeof`-specific follow-up).

Tests: `internal/executor/pg_typeof_oid_test.go`'s `TestPgTypeofOIDCast`
(builtin/numeric/text/bool/char/unknown OIDs) and
`TestPgTypeofPlainDisplayUnaffected` (uncast display text unchanged);
`internal/planner/pg_typeof_test.go`'s `TestExprTypePgTypeofIsRegtype`;
`internal/server/regtype_output_test.go`'s `TestTypeOIDForRegtype` and
`TestAppendTypedCellTextRegtypeRendersName` (invalid-OID/unknown/builtin/
user-enum cases).

Gates: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/server/... ./internal/catalog/...
./internal/parser/...` all PASS (no regressions); `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=33); live side-by-side verification against real PostgreSQL
18.3 (see above).

## Follow-up: `userTypeNameForOID` schema-visibility (2026-07-06, loop #17)

Closes the "newly observed, pre-existing, out of scope" gap recorded in the
`pg_typeof(...)::oid` follow-up above: `userTypeNameForOID`
(`internal/executor/expr.go`) unconditionally prefixed every user-defined
type name with `"public."`, regardless of whether `"public"` was actually
visible on the effective `search_path` — diverging from real PostgreSQL's
`regtypeout`/`format_type`, which only schema-qualifies when the type isn't
visible unqualified (mirroring `regproc`/`regoperator`'s existing
`regObjectSchemaVisible` check).

**Fix:** `userTypeNameForOID(cat, oid, qualify bool)` gained a `qualify`
parameter — the `"public."` prefix is added only when the caller determines
`"public"` is not visible. All three executor-package callers now pass
`!regObjectSchemaVisible(ctx, "public")`:

- the `::regtype` cast's OID→name direction (`CastExpr`'s `KindString` and
  `KindInt` cases, `internal/executor/expr.go`)
- `format_type`'s built-in-fallback path
- `RegtypeName(cat, oid, qualify bool)` (also gained the parameter) — used by
  the wire-output layer for a plain `SELECT pg_typeof(...)`/regtype-typed
  column

Since `internal/server/dispatch.go`'s `appendTypedCellText` has no
`executor.Context` (only a `getSetting func(name string) (string, bool)`
threaded in from the caller's session — `ctx.GetSetting` in the simple-query
path, `ectx.GetSetting` in the extended-query path), it gained a new
`publicSchemaVisible(getSetting)` helper mirroring
`regObjectSchemaVisible`'s search_path-parsing logic (an explicitly empty
search_path, as pg_dump always uses, correctly yields "not visible" rather
than defaulting back to `public`, unlike the pre-existing
`searchPathSchemas(sess)` used for table-name resolution).

Verified live against a real running PostgreSQL 18.3 instance side-by-side
(real PG on a throwaway `initdb`, goopg on a throwaway data dir, both torn
down after the session): for a public-schema `CREATE TYPE mood AS ENUM (...)`,
both `'mood'::regtype` and `format_type(<oid>, -1)` render bare `mood` under
the default search_path, `public.mood` under `search_path=''`, and
`public.mood` under `search_path=other_schema` (public not on the path) —
byte-identical between goopg and real PG in all three cases; `pg_typeof(...)`
plain display and `SET`/`RESET search_path` round-tripping unaffected.

Tests: `internal/executor/user_type_oid_name_test.go`'s
`TestUserTypeNameForOIDAllKinds` extended to cover both `qualify=true` and
`qualify=false` across all ten OID forms; new
`internal/executor/regtype_format_type_schema_visibility_test.go`'s
`TestRegtypeFormatTypeSchemaVisibility` (live `::regtype`/`format_type` query
execution across three search_path scenarios); new
`internal/server/regtype_output_test.go`'s
`TestAppendTypedCellTextRegtypeSchemaQualification` (wire-output layer, same
three scenarios); `TestAppendTypedCellTextRegtypeRendersName`'s existing
user-enum case updated from `"public.mood"` to `"mood"` (nil `getSetting`
now correctly means "default search_path", not "always qualify").

Gates: `go build ./...` clean; `go vet ./...` clean on touched packages;
`go test ./internal/executor/... ./internal/server/... ./internal/catalog/...
./internal/planner/... ./internal/parser/...` all PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=33); live side-by-side
verification against real PostgreSQL 18.3 (see above).
