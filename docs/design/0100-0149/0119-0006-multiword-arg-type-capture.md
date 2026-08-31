# M0119-0006 (76th slice) — multi-word built-in type names in CREATE FUNCTION args

Closes deferral ledger rows 1349 (the 74th slice's carry-forward: multi-word
type names store the LAST word) and the syntax-error half of that row
(`timestamp with time zone` / `time with time zone` were syntax errors in
CREATE FUNCTION args).

## The gap

`parseArgNameAndType` (internal/parser/function.go) disambiguates the
`ident ident` form — is `a b` an unnamed arg of type `b` with a name `a`, or
some other shape? — with a heuristic: if the token after the first identifier
is an identifier-like token, the first is treated as an **arg name**. That
heuristic misread the FIRST word of a multi-word built-in type as an arg name:

- `CREATE FUNCTION f(bit varying)` captured arg `name="bit"` `type="varying"`
  — the arglist rendered `f(varying)` where PG 18.3 renders `f(bit varying)`;
- `CREATE FUNCTION f(double precision)` captured `name="double"`
  `type="precision"` — rendered `f(precision)`;
- `CREATE FUNCTION f(character varying)` rendered `f(varying)`;
- `CREATE FUNCTION f(timestamp with time zone)` was a **syntax error** — the
  continuation word `with` is the `KwWith` keyword, which the heuristic's
  identifier check rejects outright.

The parser DID know how to parse multi-word types (`parseMultiWordTypeName`,
`parseColumnType` — used by CREATE TABLE columns), but the arg-list path never
handed control to it.

## The fix

New `isMultiWordTypeStart(nameTok string, next Token) bool` in
internal/parser/function.go recognizes a multi-word-type leader
(`double`→`precision`, `character`→`varying`, `bit`→`varying`,
`timestamp`/`time`→`with time zone`/`without time zone`,
`interval`→`year|month|day|hour|minute|second`) by checking the NEXT token:

```go
func isMultiWordTypeStart(nameTok string, next Token) bool {
	switch strings.ToLower(nameTok) {
	case "double":
		return next.Kind == TokenIdent && strings.EqualFold(next.Value, "precision")
	case "character":
		return next.Kind == TokenIdent && strings.EqualFold(next.Value, "varying")
	case "bit":
		return next.Kind == TokenIdent && strings.EqualFold(next.Value, "varying")
	case "timestamp", "time":
		// "with" is the KwWith keyword; "without" is an ordinary identifier
		return (next.Kind == TokenKeyword && next.Keyword == KwWith) ||
			(next.Kind == TokenIdent && strings.EqualFold(next.Value, "without"))
	case "interval":
		return next.Kind == TokenIdent && intervalTypmodField[strings.ToLower(next.Value)]
	}
	return false
}
```

`parseArgNameAndType` consults it BEFORE assigning an arg name: when the leader
+ continuation form matches, it rewinds `p.idx = save` and lets
`parseColumnType` consume the whole multi-word spelling (the canonical collapse
`bit varying`→`varbit`, `double precision`→`float8`, `timestamp with time
zone`→`timestamptz`, `time with time zone`→`timetz`, `interval year to month`
→`interval` with the packed typmod) — the same collapse CREATE TABLE columns
already used. The arg gets `Name=""` (bare, unnamed) and the full type, exactly
as if the user had written the canonical single-word name.

The output side needs no change: the executor's `regprocedureArglist` renders
the canonical name through the shared `catalog.ArgTypeDisplayAlias`
(74th slice), which maps `varbit`→`bit varying`, `float8`→`double precision`,
`timestamptz`→`timestamp with time zone`, `timetz`→`time with time zone` — so
the stored canonical name round-trips back to the user's SQL spelling
byte-identically.

## Verification

- Unit: `TestParseCreateFunctionMultiWordArgTypes` (internal/parser/
  function_test.go) pins six bare multi-word type cases (`bit varying`,
  `character varying`, `double precision`, `timestamp with time zone`,
  `time with time zone`, `interval year to month`) plus three named-arg cases
  (`a bit`, `b double precision`, `c timestamp with time zone` — the named form
  must still parse the name correctly).
- Live E2E on a throwaway goopg server (port 5533) + byte-identical PG 18.3
  oracle (port 5534), using `tmp/multiword-arg-oracle.sql`: the seven
  CREATE FUNCTIONs with multi-word arg types all succeed, and
  `SELECT oid::regprocedure` renders byte-identical signatures on both engines
  — `f_vchar(bit varying)`, `f_cvarchar(character varying)`,
  `f_dp(double precision)`, `f_ts(timestamp with time zone)`,
  `f_t(time with time zone)`, `f_int(interval year to month)`, and the named
  `f_named(a bit, b double precision)`.
- The created functions are callable with matching arg types, and
  `DROP FUNCTION f_vchar(bit varying)` (the multi-word signature) succeeds on
  both engines — the parser round-trips the same spelling for DROP as CREATE.
- Gates: package suites, pre-commit units, `TestPort_RegressSuite`,
  `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all PASS.

## Scope notes

- `nchar` / `national character [varying]` / `char varying` (PG aliases of
  `character`/`character varying`) are NOT added to `isMultiWordTypeStart` —
  re-filed as deferral row 1351 (the char/arglist-OID row), which also covers
  the still-open `char` vs `"char"` rendering gap.
- The interval case covers only the field forms `intervalTypmodField` already
  knows (`year|month|day|hour|minute|second`); `interval year to month` and
  `interval day to second` are the PG-recognized composites and both work.
- This is the parser-side fix for rows 1349's first half. The second half —
  the arglist carrying the arg's resolved OID so a bare `char` is told from
  OID-18 `"char"` — is a separate catalog-representation change (row 1351).
