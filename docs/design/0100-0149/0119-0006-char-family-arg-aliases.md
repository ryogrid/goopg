# M0119-0006 (77th slice) — SQL national-char aliases in CREATE FUNCTION args + bare-`character` column typmod

Closes the first half of deferral ledger row 1351 (the 76th slice's
carry-forward: `char varying` / `nchar` / `national character [varying]` — PG
aliases of `character varying` / `character` — were not yet accepted as bare
types in CREATE FUNCTION args). Along the way it also fixes a pre-existing
goopg divergence surfaced by the same grammar family: a bare `character` /
`nchar` / `national character` COLUMN was `character(-1)` (unbounded) where PG
18.3 makes it `character(1)`.

## The gap

The 76th slice extended `isMultiWordTypeStart` / `parseMultiWordTypeName` to the
`character varying` spelling, but the rest of the `character` nonterminal
(gram.y:14720-14781) was still uncovered:

| SQL spelling | PG meaning | before this slice |
|---|---|---|
| `char varying` | `character varying` (varchar) | arg name="char" type="varying` |
| `nchar` | `character` (bpchar) | arg name="nchar" type missing → syntax error |
| `nchar varying` | `character varying` | syntax error |
| `national character` | `character` | syntax error |
| `national char` | `character` | syntax error |
| `national character varying` | `character varying` | syntax error |

Verified against the PG 18.3 oracle first: `char`/`nchar`/`national
character`/`national char` are all bpchar aliases (regprocedure drops the typmod
so every `f(nchar)` renders `f(character)`); each of `char varying` /
`nchar varying` / `national character varying` / `national char varying` is
varchar (renders `f(character varying)`); and `f(char int)`, `f(nchar int)`,
`f(character int)`, `f(national int)` are all PG **syntax errors** — these
keywords can never start an arg name.

A second, independent gap surfaced during live probing: goopg's
`parseColumnType` stamped only bare `char` with the grammar-default length 1
(gram.y `CharacterWithoutLength` → bpchar typmod 1). Bare `character` and the
national aliases collapsed to `"character"` (canonical) but got typmod -1, so
`CREATE TABLE t (d character)` was `character(-1)` where PG makes it
`character(1)`.

## The fix

### `isMultiWordTypeStart` (internal/parser/function.go)

`character`, `char`, `nchar` — the plain and SQL national aliases of the
bpchar/varchar family — now return `true` whenever the NEXT token is an
identifier. A following `varying` continues the multi-word type; a following
OTHER identifier is PG's rejected shape (`f(char int)`), so we still rewind and
let `parseColumnType` consume the leading word — the dangling ident then errors
out exactly as it does on PG. `national` is treated the same way (its
continuations are `character`/`char`). Previously only the exact `character`
→`varying` pair was handled.

```go
case "character", "char", "nchar":
    // character/char/nchar — plain and SQL national aliases of the
    // bpchar/varchar family (gram.y `character: CHARACTER|CHAR_P|NCHAR
    // opt_varying`). A following `varying` continues the multi-word type
    // name; a following OTHER identifier PG rejects as a syntax error
    // (`CREATE FUNCTION f(char int)`), because these keywords can never
    // start an arg name — return true so we rewind and let
    // parseColumnType consume the leading word, and the dangling ident
    // then errors out the same way.
    return next.Kind == TokenIdent
case "national":
    return next.Kind == TokenIdent
```

### `parseMultiWordTypeName` (internal/parser/ddl.go)

The canonical collapse now knows the whole national family:

```go
case "character", "char":
    if p.acceptIdentKeyword("varying") {
        return "varchar"
    }
case "nchar":
    if p.acceptIdentKeyword("varying") {
        return "varchar"
    }
    return "character" // bare `nchar` ≡ `character` (bpchar)
case "national":
    if p.acceptIdentKeyword("character", "char") {
        if p.acceptIdentKeyword("varying") {
            return "varchar"
        }
        return "character"
    }
```

Because every spelling collapses to the canonical `character`/`varchar`, the
output side needs no change: the executor's `regprocedureArglist` renders those
names through the shared `catalog.ArgTypeDisplayAlias` (74th slice,
`varchar`→`character varying`), so `f(nchar varying)` round-trips to
`f(character varying)` and `f(national character)` to `f(character)` —
byte-identical to PG.

### `parseColumnType` bare-`character` typmod stamp (internal/parser/ddl.go)

The grammar-default length-1 stamp (`CharacterWithoutLength` → bpchar typmod 1)
now covers `character` too, alongside the existing `char` arm:

```go
if first.Kind != TokenQuotedIdent && len(ct.Args) == 0 &&
    (strings.EqualFold(ct.Name, "char") || strings.EqualFold(ct.Name, "character")) {
    ct.Args = []int64{1}
}
```

The national aliases reach this arm through `parseMultiWordTypeName`'s collapse
to `"character"`. `bpchar` spelled directly still takes typmod -1 and is
deliberately NOT stamped (codec.go:184-196 documents the two paths). The cast
path is untouched — `synthesizeBareCharTypmod` still stamps only `char`, which
matches PG's `ConstCharacter` clearing the typmod in cast positions, and the
live `'cd'::nchar(3)` → `[cd]` padding probe agrees on both engines.

## Verification

- Unit: `TestParseCreateFunctionCharFamilyArgTypes` (internal/parser/
  function_test.go) — 11 success cases pinning the parsed arg Name / typeName /
  typmod for `f(char varying)`, `f(nchar)`, `f(nchar varying)`, `f(nchar(5))`,
  `f(national character)`, `f(national char)`, `f(national character varying)`,
  `f(national char varying)`, the named forms `f(a nchar)` / `f(c char varying)`,
  and `f(named national character, national character varying)`; plus 4 error
  cases — `f(char int)`, `f(character int)`, `f(nchar int)`, `f(national int)`
  must all raise syntax errors (PG parity).
- Live E2E on a throwaway goopg server (port 5533): `CREATE TABLE t_char_alias
  (c char, d character, e nchar, f char varying, g national character,
  h nchar(5), i national character varying(10))` then
  `format_type(atttypid, atttypmod)` yields
  `character(1)/character(1)/character(1)/character varying/character(1)/
  character(5)/character varying(10)` — byte-identical to the PG 18.3 oracle
  (5534). The ten CREATE FUNCTIONs with the alias spellings all succeed and
  `oid::regprocedure` renders `f_charvar(character varying)`, `f_nchar(character)`,
  `f_ncharvar(character varying)`, `f_nchar5(character)`,
  `f_natchar(character)`, `f_natchar2(character)`,
  `f_natcharvar(character varying)`, `f_natcharvar2(character varying)`,
  `f_named(character,character varying)` — byte-identical to PG.
- Gates: package suites, pre-commit units, `scripts/tpch-spotcheck.sh`
  (Q12=2, Q13=35) all PASS.

## Scope notes

- Row 1351's SECOND half remains open (deferral, unchanged): the regprocedure
  arglist carries only the arg's NAME, so a bare `char` arg — parser-stamped
  bpchar-like `Args=[1]`, matching PG's parser — is indistinguishable from
  OID-18 `"char"` and still renders `"char"` where PG renders `character`. That
  needs OID-per-arg capture, a catalog-representation change.
- Unrelated pre-existing catalog note observed during probing (not introduced
  here): `CREATE OR REPLACE FUNCTION` of a pre-existing routine can hit
  "catalog update: freshly extended page did not accept tuple" when the pg_proc
  heap page is full — a pg_proc heap-page-extension limitation, not a parser
  issue.
