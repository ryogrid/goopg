# M0134-0106 — conversion.sql: user-conversion catalog fixes

Status: PARKED (case remains `failed`; three contained fixes shipped, dominant
blocker deferred). Sized 2026-08-24.

## Case

`postgres/src/test/regress/sql/conversion.sql` exercises `CREATE [DEFAULT]
CONVERSION`, `COMMENT ON CONVERSION`, and `DROP CONVERSION`, then spends the
majority of its body round-tripping byte sequences through a C-language
test harness (`test_enc_setup()`, `test_enc_conversion(...)`, wrapped by a
SQL-level `test_conv()` table-valued function defined in
`postgres/src/test/regress/sql/conversion.sql`'s companion expected file via
`regress.so`) across MULE_INTERNAL/KOI8R/SJIS/BIG5/EUC_JP/UTF8 encoding pairs.

Diff at HEAD (`scripts/pg-regress-runner.sh --verbose conversion`): 613 lines,
35 `^+ERROR` / 2 `^-ERROR`.

## Root causes found (three contained, one dominant REFACTOR-tier)

1. **`CREATE DEFAULT CONVERSION` missing encoding-pair uniqueness check.**
   Real PG's `ConversionCreate` (`postgres/src/backend/catalog/pg_conversion.c:66-79`)
   additionally requires — when `condefault=true` — that no other default
   conversion in the namespace already covers the same
   `(conforencoding, contoencoding)` pair, via `FindDefaultConversion`,
   independent of name: `default conversion for %s to %s already exists`.
   goopg's `catalog.InMemory.CreateConversion` only checked name uniqueness.
   Fixed: added the encoding-pair scan (`internal/catalog/catalog.go`,
   `CreateConversion`), using the existing `EncodingIDToName` for the
   `pg_encoding_to_char`-equivalent message rendering.

2. **`COMMENT ON CONVERSION` had no case at all.** `execCommentOn`'s switch
   (`internal/executor/operators_ddl.go`) enumerates every commentable object
   kind explicitly and falls through to a silent `return nil` for any kind
   without a case — "conversion" was simply missing, so
   `COMMENT ON CONVERSION nonexistent IS '...'` silently succeeded instead of
   PG's `conversion "..." does not exist` (42704, mirrors
   `GetCommentObjectAddress` in `objectaddress.c`), and a comment on a real
   conversion was silently discarded rather than reaching `pg_description`.
   Fixed: added a `case "conversion"` resolving via
   `im.FindConversion(...)` and storing under `pgConversionRelOID` (2607).

3. **`DROP CONVERSION` on a real conversion raised a false "does not
   exist".** The DROP dispatch's `conversion` branch called
   `im.DropConversion(...)` and, on success, stamped `xmax` on the
   `pg_conversion` heap row — but never `return`ed. Execution fell through to
   a generic `im.DropCompatObject(objType, s.Names[0].String())` gate keyed by
   the CREATE-time compat-registry string (schema-qualified, e.g.
   `"public.mydef"`), while the DROP statement supplies the bare name
   (`"mydef"`); the mismatch made `DropCompatObject` report "not found", and
   execution fell all the way through to the final unconditional
   `"does not exist"` error — even though the conversion had just been
   dropped successfully. Fixed: `return nil` (after best-effort
   `DropCompatObject` cleanup) immediately on a successful `DropConversion`,
   mirroring the sibling `text search dictionary`/`text search configuration`
   branches in the same switch, which already did this correctly.

4. **Dominant blocker (REFACTOR-tier, not attempted this loop):**
   `CREATE FUNCTION test_enc_conversion(bytea, name, name, bool, validlen OUT
   int, result OUT bytea) AS :'regresslib', 'test_enc_conversion' LANGUAGE C
   STRICT;` fails to parse — goopg's `CREATE FUNCTION` grammar requires an
   explicit `RETURNS` clause; real PG derives the return type from `OUT`
   parameters when `RETURNS` is omitted (a `RECORD`-shaped composite here).
   Even granting that parse, the function body loads `test_enc_conversion`
   from a compiled `.so` (`LANGUAGE C`) — goopg has no dynamic C-extension
   loading engine at all, so the function could never actually execute.
   `test_conv(...)`, the SQL-level table-valued wrapper the bulk of the file
   calls, in turn depends on that C function, so this single gap masks ~85%
   of the remaining diff (all `SELECT ... (test_conv(...)).*` blocks across
   six encoding pairs). No contained slice of this bucket exists — implicit
   `RETURNS`-from-`OUT` is table-valued-function/parser work, and a working
   C-loader is an entirely new subsystem.

## Verification

`scripts/pg-regress-runner.sh --verbose conversion`: 613 → 602 diff lines,
35 → 34 `^+ERROR`, 2 → 1 `^-ERROR`. The DEFAULT CONVERSION duplicate-detection
line and the `COMMENT ON CONVERSION myconv_bad` error line now match; the
false `DROP CONVERSION mydef` error is gone. Remaining lines are entirely the
`test_conv`/C-loader bucket.

## Resume point

Re-arm when a `LANGUAGE C` dynamic-extension-loading milestone lands (there is
none currently planned) — at that point also add implicit-`RETURNS`-from-`OUT`
support to the `CREATE FUNCTION` grammar (`internal/parser/*` — the function
clause parser currently treats `RETURNS` as mandatory).
