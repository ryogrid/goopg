# root-0034 — `float` is a grammar-level alias, not a type name: the opt_float reduction

Status: accepted · 2026-07-28 · branch `tpcds-fix2`

Closes M-NIGHTLY item (b): the surviving `regress/index_including` divergence
left by root-0029/-0031/-0032/-0033. Code: `internal/parser/{ddl,select,parser}.go`,
`internal/server/{copy,dispatch,dispatch_extended}.go`.

## 1. Symptom

`TestPort_RegressSuite/index_including` reported `deferred: output mismatch;
normalization rules need extension` in every nightly since 20260719, and — unlike
`errors`/`portals_p2`/`select` — it reproduced with the cluster demonstrably
alive (case #88 of the alphabetical prefix, no restart in the log). Captured with
`GOOPG_REGRESS_DIFF_DIR`, the whole 378-line divergence is four lines, in §10 of
the case ("Test coverage for names stored as cstrings in indexes"):

```
 CREATE TABLE nametbl (c1 int, c2 name, c3 float);
 CREATE INDEX nametbl_c1_c2_idx ON nametbl (c2, c1) INCLUDE (c3);
 INSERT INTO nametbl VALUES(1, 'two', 3.0);
 …
 SELECT c2, c1, c3 FROM nametbl WHERE c2 = 'two' AND c1 = 1;
-  c2  | c1 | c3        →   c2 | c1 | c3
- -----+----+----           ----+----+----
-  two |  1 |  3            (0 rows)
- (1 row)
```

The case's title, its position in `index_including`, and the `INCLUDE (c3)`
index all point at an index-only scan over a `name`-typed leading key. That
reading is wrong. Reduced against a throwaway cluster, the row is gone from a
**plain sequential scan** — before any index is consulted, and with no index on
the table at all:

```
CREATE TABLE n1 (c1 int, c2 name, c3 float);
INSERT INTO n1 VALUES(1, 'two', 3.0);   -- INSERT 0 1
SELECT * FROM n1;                       -- (0 rows)
```

Bisecting the column list isolates it to one token: the table survives with
`c3 float8`, `c3 double precision`, `c3 real`, `c3 float4` and `c3 numeric`,
and loses its row with `c3 float`, `c3 float(10)` and `c3 float(25)`.

## 2. Root cause — `float` is not a type, it is a grammar production

PostgreSQL has no `float` in `pg_type`. `FLOAT [ ( precision ) ]` is resolved
**entirely inside the grammar**, by `opt_float`
(`postgres/src/backend/parser/gram.y`):

| spelling | reduces to |
|---|---|
| `float` | `SystemTypeName("float8")` |
| `float(p)`, 1 ≤ p ≤ 24 | `SystemTypeName("float4")` |
| `float(p)`, 25 ≤ p ≤ 53 | `SystemTypeName("float8")` |
| `float(p)`, p < 1 | ERROR 22023 `precision for type float must be at least 1 bit` |
| `float(p)`, p > 53 | ERROR 22023 `precision for type float must be less than 54 bits` |

The reduction is a *rename*: `SystemTypeName` builds a bare `TypeName` with no
typmod, so the precision is consumed by the production and never reaches
`pg_attribute.atttypmod` (PG stores `atttypmod = -1` for `float(24)`).

goopg's parser had no such reduction. It stored the literal token, so `float`
travelled all the way into `catalog.TypeNameToOID`
(`internal/catalog/codec.go`), whose closing arm is

```go
default:
    return OIDText // safe fallback
```

The column was therefore created as **text** — `atttypid = 25`, `attlen = -1`,
`\d` renders `text`. Meanwhile the executor's own, independent type tables
never went through that function and *did* know the name:
`internal/executor/codec.go:482` and `expr.go:3035` both list `"float"`
alongside `float8`/`double precision`. So the write path encoded an 8-byte
IEEE-754 fixed-width datum while the catalog described a varlena text column.

This is Hard-won Rule #2 (sibling paths must change together) in its
encode↔decode form, and it fails silently and totally: `INSERT` reports
`INSERT 0 1`, the tuple is written and committed, and every subsequent read
of the table returns zero rows. The negative control makes the split visible —
with the fix disabled, the same test renders the column as
`"-DT\xfb!\t@"`, which is exactly the little-endian float64 encoding of
3.14159265358979 being handed to a text decoder.

Two secondary divergences fell out of the same gap:

* `CREATE DOMAIN d AS float` produced `typbasetype = text`;
* `x::float(24)` produced double precision where PG produces real (the bare
  `x::float` cast happened to work, because the executor's own name table
  resolves the token — one of the two siblings, again).

## 3. Fix

Perform PG's reduction where PG performs it: in the parser, before any type
name escapes into the catalog.

`normalizeFloatTypeName(name, args, pos)` (`internal/parser/ddl.go`) implements
`opt_float` verbatim, including both error arms and the dropping of the
precision, and preserves a trailing `[]` (the cast paths append the array
suffix to the type name before typmods are parsed). It is wired into the four
places goopg parses a type name that can carry a typmod:

| site | grammar position |
|---|---|
| `parseColumnType` (`ddl.go`) | CREATE TABLE columns, ALTER TABLE … TYPE, function args/returns, and — via `bodyParser.parseTypeRef`, which round-trips through `CREATE TABLE _ (_ <type>)` — PL/pgSQL DECLARE |
| `parseCreateDomain` (`ddl.go`) | CREATE DOMAIN's AS clause (its own copy of the type-name grammar) |
| `parseCastTail` (`select.go`) | `x::float(p)` |
| `parseCastFuncExpr` (`select.go`) | `CAST(x AS float(p))` |

Each site skips the reduction when the token was a **quoted** identifier or the
name was schema-qualified: PG reaches `opt_float` only from the `FLOAT_P`
keyword, so `"float"` and `myschema.float` name a user type and must not be
rewritten.

`catalog.TypeNameToOID` is deliberately left alone. Adding a `"float"` arm
there would be the non-faithful fix — upstream's `typenameTypeId` cannot
resolve `float` either, precisely because the grammar has already eliminated
it.

### SQLSTATE

`opt_float`'s two rejections are `ERRCODE_INVALID_PARAMETER_VALUE` (22023), not
the parser's default `42601`. `parser.SyntaxError` gained an optional `Code`
field (a bare string, so `internal/parser` stays free of an `sqlstate` import)
and the server's three parse-error call sites now route through a new
`syntaxErrorCode` helper next to the existing `syntaxErrorMsg`. Both messages,
the caret position and the SQLSTATE are byte-identical to PG:

```
ERROR:  22023: precision for type float must be at least 1 bit
LINE 1: CREATE TABLE fbad3(a float(0));
                             ^
```

## 4. Verification

* `internal/parser/float_typename_test.go` — the reduction table, the
  no-lingering-typmod rule, both error arms (message **and** 22023), both cast
  spellings, CREATE DOMAIN, the array suffix, and the quoted-`"float"`
  carve-out.
* `internal/executor/float_type_alias_roundtrip_test.go` — the defect itself,
  end to end through DDL → INSERT → SELECT on the exact `index_including` §10
  fixture, plus a π-to-15-digits round trip that distinguishes float4-backed
  from float8-backed storage. **Negative control**: with the reduction
  short-circuited, the round-trip test returns 0 rows and the precision test
  renders the raw IEEE-754 bytes — so neither test is vacuous.
* `go test ./internal/parser/ ./internal/executor/` PASS.
* Live cluster: the full §10 fixture returns its row; `float`/`float(1)`/
  `float(24)`/`float(25)`/`float(53)` map to
  `double precision`/`real`/`real`/`double precision`/`double precision` with
  `atttypmod = -1`; `float[]` → `double precision[]`; `CREATE DOMAIN … AS
  float(10)` → `real`; `float(0)`/`float(54)` raise PG's messages with 22023.
* `TestPort_RegressSuite/index_including` **PASSES** in full-suite ordering
  (88-case alphabetical prefix, 244 s), where it had reported an output
  mismatch on every prior run.

## 5. Deferred

* PL/pgSQL variable declarations do not coerce to the declared type at all:
  `DECLARE x float := 1.5` reports `pg_typeof(x) = numeric`, and so does
  `DECLARE x float8 := 1.5`. Pre-existing and independent of this change
  (`plpgsql_runtime.go` keeps the initialiser's datum); ledger row filed.
* `catalog.TypeNameToOID`'s `default: return OIDText` fallback still turns
  *every* unknown type name into a silently-wrong text column. `float` was one
  instance; the general failure mode is unbounded. Ledger row filed.
