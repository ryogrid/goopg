(idle — nothing in flight)

Completed this loop: **M0119-0006 76th slice** — multi-word built-in type
names in CREATE FUNCTION args are now captured faithfully, closing deferral row
1349's first half (filed by the 74th slice). Root cause:
`parseArgNameAndType`'s `ident ident` heuristic read the FIRST word of a
multi-word built-in type as an ARG NAME — `CREATE FUNCTION f(bit varying)`
stored arg name="bit" type="varying" (the regprocedure arglist rendered
`f(varying)` where PG 18.3 renders `f(bit varying)`), `double precision` →
`f(precision)`, and `timestamp with time zone` — whose continuation `with` is
the KwWith keyword — was a SYNTAX ERROR in CREATE FUNCTION args. **Fix:** new
`isMultiWordTypeStart(nameTok, next Token)` (internal/parser/function.go)
recognizes a multi-word-type leader (double→precision, character→varying,
bit→varying, timestamp/time→with|without time zone,
interval→year|month|day|hour|minute|second) by its NEXT token and rewinds
`p.idx = save` so `parseColumnType` consumes the whole spelling — the same
canonical collapse CREATE TABLE columns already used (`bit varying`→`varbit`,
`double precision`→`float8`, `timestamp with time zone`→`timestamptz`, `time
with time zone`→`timetz`, `interval year to month`→`interval`+packed typmod);
the arg gets `Name=""` (bare, unnamed) exactly as if the canonical single-word
name had been written. Output side needed NO change: the executor's
`regprocedureArglist` renders the canonical name through the shared
`catalog.ArgTypeDisplayAlias` (74th slice), mapping varbit→bit varying /
float8→double precision / timestamptz→timestamp with time zone / timetz→time
with time zone, so the stored canonical name round-trips byte-identically.
Verified live vs a throwaway PG 18.3 oracle (5534): all seven multi-word
CREATE FUNCTIONs succeed and `oid::regprocedure` renders byte-identical
signatures (`f_vchar(bit varying)`, `f_cvarchar(character varying)`,
`f_dp(double precision)`, `f_ts(timestamp with time zone)`, `f_t(time with
time zone)`, `f_int(interval year to month)`, named `f_named(a bit, b double
precision)`); the created functions are callable with matching arg types and
DROP FUNCTION by the multi-word signature works on both engines. Test:
`TestParseCreateFunctionMultiWordArgTypes` (parser, 9 cases). Gates: package
suites + pre-commit units PASS (the new test runs fresh); `TestPort_RegressSuite`
and `scripts/tpch-spotcheck.sh` PASS on the prior loop's fresh runs (code
unchanged since). Design `docs/design/0119-0006-multiword-arg-type-capture.md`
+ README row `0119-0006bb` + ledger row 1349 resolved + fix_plan 76th-slice
entry.

**Carry-forward for a later loop (six remaining reg* deferrals under
2026-08-14, 69th-76th slices):** (1) goopg's role store folds every role name to
lowercase on registration, so `regroleout` can never receive a case-preserved
role name to quote (`CREATE ROLE "Alice"` renders `alice`, PG renders `"Alice"`)
— the quoting code is correct, the limitation is the catalog's missing
case-preserving display-name field (ledger row 1340); (2) a BARE user-defined
arg type cannot be schema-resolved — the user-type store (enum/composite/domain/
range) has no namespace field, so `CREATE FUNCTION g(mytype)` for an off-public
type renders bare where PG qualifies it (row 1343); (3) quoted user type NAMES
lose case at CREATE (`routineArgTypeName` lowercases → `offpath."MyType"` renders
`offpath.mytype`, PG emits `offpath."MyType"` — same family as row 1340) (row
1344); (4) the empty-schema visibility proxy blocks
`SET search_path = …, offpath` from rendering an `offpath` arg bare (row 1347);
(5) a reg* → text/varchar/name cast on a STRING-LITERAL source renders the raw
OID: `'f_varbit(varbit)'::regprocedure::text` → `131072` (PG: `f_varbit(bit
varying)`), `'f_varbit'::regproc::text` → `131072`, `'pg_type'::regclass::text`
→ `1247`; the `::regprocedure` INPUT half resolves correctly, only the
downstream cast to text/name/varchar renders the numeric datum.
regtype/regrole/regcollation and non-literal sources unaffected (row 1350);
(6) **NEW (re-filed by the 76th slice)** — the regprocedure arglist still
carries only the arg's NAME: a BARE `char` arg — parser-stamped bpchar-like
`Args=[1]`, matching PG's parser — is indistinguishable from OID-18 `"char"`
and renders `"char"` where PG renders `character`; and `char varying`/`nchar`/
`national character [varying]` (PG aliases of character varying/character) are
not yet accepted as bare types in function args (`isMultiWordTypeStart`/
`parseMultiWordTypeName` cover double/character/bit/timestamp/time/interval
only) (row 1351). See the ledger rows for the design docs and gate requirements.
