(idle — nothing in flight)

Completed this loop: **M0119-0006 77th slice** — SQL national-char aliases in
CREATE FUNCTION args + the tied bare-`character` column typmod fix, closing
deferral row 1351's first half (re-filed by the 76th slice). **Part (b):**
`char varying` / `nchar [varying]` / `national character [varying]` /
`national char [varying]` — PG's aliases of `character` / `character varying`
(gram.y `character` nonterminal: `CHARACTER|CHAR_P|NCHAR opt_varying`) — were
not yet accepted as bare types in function args: the 76th slice's
`isMultiWordTypeStart` covered `character→varying` but `f(nchar)`,
`f(national character)`, `f(char varying)` were syntax errors / mis-parsed.
**Fix:** `isMultiWordTypeStart` (internal/parser/function.go) now treats
`character`/`char`/`nchar`/`national` as multi-word-type leaders whenever the
NEXT token is an identifier (a following `varying` continues the type; a
following OTHER identifier is PG's syntax-error shape `f(char int)` — we still
rewind and let `parseColumnType` consume the leading word, and the dangling
ident errors out exactly as on PG), and `parseMultiWordTypeName`
(internal/parser/ddl.go) collapses every spelling to the canonical
`character`/`varchar` (`nchar`→`character`, `national character
[varying]`→`character [varying]`, `char varying`→`varchar`). The output side
needed NO change: the executor's `regprocedureArglist` renders the canonical
name through the shared `catalog.ArgTypeDisplayAlias` (74th slice), so
`f(nchar varying)` round-trips to `f(character varying)` byte-identically.
**Part (a / tied):** `parseColumnType`'s grammar-default length-1 stamp
(gram.y `CharacterWithoutLength` → bpchar typmod 1) covered only bare `char`,
so a bare `character`/`nchar`/`national character` COLUMN was `character(-1)`
(unbounded) where PG 18.3 makes it `character(1)` — the stamp now fires for
`character` too (`bpchar` spelled directly and the cast path are deliberately
untouched; `synthesizeBareCharTypmod` still stamps only `char`, matching PG's
`ConstCharacter` clearing typmods in cast positions). Verified live vs a
throwaway PG 18.3 oracle (5534): `CREATE TABLE t_char_alias (c char,
d character, e nchar, f char varying, g national character, h nchar(5),
i national character varying(10))` → `format_type(atttypid, atttypmod)`
yields `character(1)/character(1)/character(1)/character varying/character(1)/
character(5)/character varying(10)` on BOTH engines (the `d`/`e`/`g` divergence
was a stale table from the pre-fix binary at first probe; a clean
DROP+recreate after restart confirms parity); the ten alias-spelling CREATE
FUNCTIONs all succeed and `oid::regprocedure` renders byte-identical
signatures (`f_charvar(character varying)`, `f_nchar(character)`,
`f_ncharvar(character varying)`, `f_nchar5(character)`, `f_natchar(character)`,
`f_natchar2(character)`, `f_natcharvar(character varying)`,
`f_natcharvar2(character varying)`, `f_charvar_named(character
varying,character varying)`, `f_named(character,character varying)`).
Test: `TestParseCreateFunctionCharFamilyArgTypes` (parser, 11 success + 4
syntax-error cases). Design
`docs/design/0119-0006-char-family-arg-aliases.md` + README row `0119-0006bc` +
ledger row 1351 updated (first half resolved, OID-per-arg half re-filed) +
fix_plan 77th-slice entry. Commit `3f99e6e5` (pgbench smoke 12824 TPS, 0
failures). Gates: package suites + pre-commit units +
`scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all PASS.

**Carry-forward for a later loop (remaining reg* deferrals under 2026-08-14,
69th-77th slices):** (1) goopg's role store folds every role name to lowercase
on registration, so `regroleout` can never receive a case-preserved role name
to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`) — the
quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (ledger row 1340); (2) a BARE user-defined
arg type cannot be schema-resolved — the user-type store (enum/composite/
domain/range) has no namespace field, so `CREATE FUNCTION g(mytype)` for an
off-public type renders bare where PG qualifies it (row 1343); (3) quoted user
type NAMES lose case at CREATE (`routineArgTypeName` lowercases →
`offpath."MyType"` renders `offpath.mytype`, PG emits `offpath."MyType"` — same
family as row 1340) (row 1344); (4) the empty-schema visibility proxy blocks
`SET search_path = …, offpath` from rendering an `offpath` arg bare (row 1347);
(5) a reg* → text/varchar/name cast on a STRING-LITERAL source renders the raw
OID: `'f_varbit(varbit)'::regprocedure::text` → `131072` (PG: `f_varbit(bit
varying)`), `'f_varbit'::regproc::text` → `131072`, `'pg_type'::regclass::text`
→ `1247`; the `::regprocedure` INPUT half resolves correctly, only the
downstream cast to text/name/varchar renders the numeric datum.
regtype/regrole/regcollation and non-literal sources unaffected (row 1350);
(6) **STILL OPEN (row 1351 second half)** — the regprocedure arglist still
carries only the arg's NAME: a BARE `char` arg — parser-stamped bpchar-like
`Args=[1]`, matching PG's parser — is indistinguishable from OID-18 `"char"`
and renders `"char"` where PG renders `character`. The parser spellings
(`char varying`/`nchar`/`national character [varying]`) landed in the 77th
slice; the remaining work is an OID-per-arg catalog-representation change
(carry the arg's resolved type OID alongside its Name and render via
format_type_be). See the ledger rows for the design docs and gate requirements.
