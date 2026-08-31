# Nodes + PL/pgSQL — Bug Review 2026-08-31

Files: datum.go, ir.go, ir_query.go, numeric_storage.go, outfuncs.go, outfuncs_query.go, readfuncs.go, readfuncs_query.go, rebuild.go, rebuild_query.go, resolver_expr.go, resolver_query.go, unsupported.go, ast.go, parser.go

Findings count: 6

---

## 1. `parser.go:parseFor` — query FOR loop scan truncates on a `loop` identifier inside the SQL text

- **Bug**: For `FOR rec IN <query> LOOP ... END LOOP`, the query text is captured by scanning tokens until `t.Keyword == parser.KwLoop && depth == 0`. But `loop` is registered as a keyword (`KwLoop`, `KwCatUnreserved` in `internal/parser/keywords.go:113`) and the SQL lexer tokenizes **every** keyword (including unreserved ones) as `TokenKeyword` (`lexer.go:214`). So a `loop` identifier used as a column name/alias inside the SELECT text is also a `TokenKeyword(KwLoop)`, and the scan stops there, silently truncating the query.
- **When it triggers**: e.g. `FOR rec IN SELECT x AS loop FROM t LOOP ...` — the scan stops at the `loop` alias, capturing only `SELECT x AS`; `FROM t` is then parsed as the loop body → wrong AST / parse failure. A `loop` column name (`SELECT loop FROM t`) hits it too. The same class of problem affects string content only if the lexer tokenized it as a keyword, which it does not for string literals (those are `TokenString`), so the practical trigger is the identifier case.
- **Fix**: The scan cannot stop on the first depth-0 `KwLoop`. Distinguish the PL/pgSQL terminator from SQL-text identifiers: track keyword nesting (e.g. don't stop inside a `SELECT`/`FROM` list), scan to `LOOP` only when the SQL clause is structurally complete, or use the main SQL parser to find the query's extent.
- **Severity**: medium

## 2. `parser.go:parseSQLStmt` — `SELECT INTO x FROM t` (no select list) yields a malformed query

- **Bug**: When `SELECT` is immediately followed by `INTO` (no select list — the SQL-level `SELECT * INTO` shorthand), the INTO-clause detection fires at the `INTO` token and the reconstructed query becomes `p.src[startPos:intoByteStart] + " " + p.src[targetsEndByte:endPos]` = `"SELECT " + " FROM t"` → `"SELECT  FROM t"`, which is not valid SQL (the `*` is missing).
- **When it triggers**: A PL/pgSQL body containing the SQL shorthand `SELECT INTO x FROM t;` (no select list). PG's PL/pgSQL grammar requires a select list in the `SELECT ... INTO target` form, so this is accepting an invalid-for-PL/pgSQL form and producing a broken query string that fails at execution rather than at parse.
- **Fix**: If no tokens occur between `SELECT` and `INTO`, emit `SELECT *` (or reject the form with a parse error, matching PG).
- **Severity**: low (invalid input, but produces a silently-broken query instead of a parse error)

## 3. `outfuncs.go:outDatum` — by-value Const with a short/nil Datum panics

- **Bug**: The by-value branch does `for i := range 8 { … int8(c.Datum[i]) … }`, indexing 8 bytes with no `len(c.Datum)` check. A `Const` with `ConstByval=true` and `len(Datum) < 8` panics with index-out-of-range.
- **When it triggers**: Only via manual construction of a byval `Const` — every internal constructor (`byvalWord`) produces exactly 8 bytes and `readDatum` always reads 8 bytes for byval, so normal operation is safe. The invariant is implicit and unenforced.
- **Fix**: Guard `len(c.Datum) >= 8` (error/panic with a clear message) or iterate over `min(8, len(c.Datum))`.
- **Severity**: low

## 4. `readfuncs.go:readDatum` — negative / zero by-reference length is silently accepted

- **Bug**: For a by-reference datum, `n := length; if length <= 0 { n = 0 }`; with `length < 0` the function returns a `nil` Datum with no error. PostgreSQL's `readDatum` treats the length as `Size` (unsigned) and would palloc-fail on a negative token; here a malformed/corrupt pg_node_tree string with a negative datum length is silently accepted.
- **When it triggers**: Corrupt `pg_attrdef.adbin` / `ev_action` input. Also, downstream `textFromVarlena` / `bitLenFromVarlena` / `bitDataFromVarlena` index `b[:4]`/`b[8:total]` without bounds checks, so a varlena whose `length` token disagrees with its internal VARSIZE field panics on the caller side (e.g. `textFromVarlena` doing `b[4:total]` with `total > len(b)`).
- **Fix**: Reject negative lengths with an error; validate the varlena header vs. the declared length in the byref readers (or guard the slice bounds in `textFromVarlena`/`bitDataFromVarlena`).
- **Severity**: low

## 5. `parser.go:parseFor` — `isQueryFor` peeks only at the first token, so an integer-range FOR whose first bound is a parenthesized sub-query misroutes

- **Bug**: The query-vs-range decision is made by peeking at the token after `IN`/`IN REVERSE` (`isQueryFor = true` only for `SELECT/INSERT/UPDATE/DELETE/WITH/EXECUTE` or `(`). A range FOR with a sub-query-ish first bound, e.g. `FOR i IN (SELECT 1) .. (SELECT 10) LOOP`, is misrouted to the *query* FOR path (because of the leading `(`), which then scans until a depth-0 `LOOP` — but the `..` between the two parenthesized bounds has no LOOP — so it consumes the entire range expression and only stops at the real `LOOP`, capturing `(SELECT 1) .. (SELECT 10)` as "SQL" and failing or mis-parsing. Conversely the query path also shares Bug 1's `loop`-keyword truncation.
- **When it triggers**: `FOR i IN (expr) .. (expr) LOOP` or any parenthesized range bound. Parenthesized bound expressions are legal PL/pgSQL integer-range FOR bounds, so this is valid input mishandled.
- **Fix**: Decide query-vs-range by looking for `..` (range) vs a query-starting keyword before the depth-0 `LOOP`, rather than by the first token alone.
- **Severity**: medium

## 6. `parser.go:parseStmt` — `LOOP`/`WHILE`/`FOR`/`IF` are only recognized when they are the *first* token; a `label: LOOP` / `<<label>>` form is not supported and mis-parses

- **Bug**: Statement dispatch in `parseStmt` switches on the current token kind/keyword. PL/pgSQL label prefixes (`<<lbl>>`, or `lbl: LOOP`) are not handled anywhere in this parser (the AST comments note labels are future scope), so a labelled loop/block falls through to `parseAssign` and errors with "expected ':=' or '='".
- **When it triggers**: Any PL/pgSQL body using a statement label (`<<top>> LOOP ... END LOOP` or `mylabel: FOR ...`).
- **Fix**: Out of scope per the AST comments (labels are explicitly deferred), so this is a documented limitation rather than a regression — noted for completeness.
- **Severity**: low (documented limitation, not a regression)

---

## Files reviewed with no functional bugs

- `ir.go`, `ir_query.go` — struct definitions; field order matches outfuncs.c.
- `datum.go` — int parsing, numeric parse/serialize (verified weight/offset/dscale arithmetic and short/long-form header round-trips by hand), date/time/era parsing, bit packing all correct. The `outfuncs`/`readfuncs` round-trip of date/time/numeric/bit Consts is symmetric.
- `numeric_storage.go` — `NumericInt64FromStoredPayload` 128-bit accumulation, `bits.Div64` preconditions, negative-range handling, and the legacy-text discrimination are all correct.
- `outfuncs.go`, `outfuncs_query.go` — per-tag field order matches the readers; `outDatum`/`readDatum` (8-byte byval, length-prefixed byref, signed byte output) round-trip; `outToken`/`unToken` escaping is invertible (over-escaping vs. PG, but round-trip-safe). (outDatum short-Datum panic noted above as finding 3.)
- `readfuncs.go`, `readfuncs_query.go` — field order symmetric with the writers; Query/RTE/perminfo/FromExpr/TargetEntry/Var/Alias validators match the emitted fixed skeleton field-for-field. (negative-length datum noted above as finding 4.)
- `rebuild.go`, `rebuild_query.go` — Const→AST rebuild paths (byval sign-extension, varlena decode, numeric text, timetz offset negation, bit-string formatting) are the correct inverses; view scope Var→column mapping is correct.
- `resolver_expr.go` — literal typing, numeric-family cast OID table, CASE common-type walk and coercion, simple-form CASE placeholder, string-fold subset, and length-coercion wrappers are correct. `caseTypeMeta`, `selectCaseCommonType` (incl. the `{int,float4}` → float4 cases) match PG behavior.
- `resolver_query.go` — view-shape gates, `selectedCols` bias (+7) and sorting, Var construction, and `coerceUnknownForOp` are correct.
- `unsupported.go` — thin correct wrapper.
- `ast.go` — struct definitions only.

## Notes on things examined and found correct (not bugs)

- `datum.go:stripEraSuffix` — `strings.ToUpper` index is position-preserving; checks on the original `s` at those indices are safe.
- `datum.go:parseBoolLiteral` — `strings.HasPrefix("true", v)` etc. is the correct prefix direction.
- `readfuncs.go:readDatum` — ignoring the length token for byval (always 8 bytes) matches PostgreSQL's `readDatum`.
- `resolver_expr.go:selectCaseCommonType` hasFloat4 → OidFloat4 — verified against PG's `select_common_type` walk for every reachable mix.
- `parser.go` STRICT/GRANT/REVOKE checks (`TokenIdent`) — confirmed these words are NOT in the lexer keyword table, so `TokenIdent` is correct; not a bug.
