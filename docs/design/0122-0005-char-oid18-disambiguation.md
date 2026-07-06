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
