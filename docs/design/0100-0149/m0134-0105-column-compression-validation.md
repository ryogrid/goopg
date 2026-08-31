# `compression.sql`: enforce `GetAttributeCompression` method/type checks (M0134-0105)

## Status: PARKED (`failed`)

## Summary

`compression.sql` sized live at a 336-line diff against the PG 18.3 oracle,
0% parity. Two divergences near the file's tail were a missing semantic
check goopg's `COMPRESSION` clause handling never performed:

```sql
CREATE TABLE cmdata2 (f1 int COMPRESSION pglz);
-- PG:    ERROR:  column data type integer does not support compression
-- goopg (before this fix): silently created the table

CREATE TABLE badcompresstbl (a text COMPRESSION I_Do_Not_Exist_Compression);
-- PG:    ERROR:  invalid compression method "i_do_not_exist_compression"
-- goopg (before this fix): silently created the table with no compression
--                          override, cascading a phantom "already exists"
--                          error into the next two statements
```

goopg's `COMPRESSION <method>` clause was recorded purely for `pg_dump`
round-tripping (`internal/parser/ddl.go`'s doc comment literally said "goopg
does not enforce the method, this is dump-fidelity only"): the parser's
`normalizeCompressionMethod` mapped any method name outside `pglz`/`lz4` to
the empty string, silently discarding both typos and legitimately-rejected
methods, and no code path ever checked the target column's type against
PG's toastability rule.

Fixed by porting real PG's `GetAttributeCompression`
(`postgres/src/backend/commands/tablecmds.c:22043-22076`, called from both
`DefineRelation` at line 1446 and `ATExecSetCompression` at line 18781):

```c
static char
GetAttributeCompression(Oid atttypid, const char *compression)
{
	if (compression == NULL || strcmp(compression, "default") == 0)
		return InvalidCompressionMethod;
	if (!TypeIsToastable(atttypid))
		ereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
				 errmsg("column data type %s does not support compression", ...)));
	cmethod = CompressionNameToMethod(compression);
	if (!CompressionMethodIsValid(cmethod))
		ereport(ERROR, (errcode(ERRCODE_INVALID_PARAMETER_VALUE),
				 errmsg("invalid compression method \"%s\"", compression)));
	return cmethod;
}
```

Two changes:

1. **Parser** (`internal/parser/ddl.go`, `normalizeCompressionMethod`): an
   unrecognized method token is now passed through lowercased (identifiers
   are already lexer-lowercased when unquoted) instead of silently
   discarded to `""`, so the executor has the original text to validate and
   report. `""`/`"default"` still normalize to `""` (clears the override,
   matching `SET COMPRESSION default`).
2. **Executor** (`internal/executor/operators_ddl.go`, new
   `validateColumnCompression`): mirrors `GetAttributeCompression` — a
   non-empty method must be exactly `"pglz"` or `"lz4"` (22023 `invalid
   compression method "%s"` otherwise), and a recognized method may only be
   set on a TOAST-aware type (0A000 `column data type %s does not support
   compression` otherwise, reusing `columnTypeStorageCode`'s `'p'`-code
   toastability test — the same helper `validateColumnStorage` already
   uses for the analogous `SET STORAGE` rule). Wired into three call sites
   mirroring `validateColumnStorage`'s existing wiring: `execCreateTable`'s
   `addCol` closure (BodyOrder path), the no-BodyOrder fallback column
   loop, and `AlterTableSetCompression`'s handler.

Verified live: both error messages now match the PG 18.3 oracle
byte-for-byte, and the phantom `badcompresstbl`/cascading `"already
exists"` divergence is gone.

Diff: 336 → 307 lines.

## Remaining buckets (ledgered, PARKED)

`compression.sql`'s dominant remaining bucket, by a wide margin, is
**`pg_column_compression(any)`** — a builtin entirely absent from goopg's
catalog. Unlike `Column.Compression` (a per-attribute catalog default),
`pg_column_compression` inspects the **per-value** TOAST compression method
recorded in a stored varlena's toast pointer/compressed-data header
(`VARATT_IS_COMPRESSED`/`TOAST_COMPRESS_METHOD`,
`postgres/src/backend/utils/adt/varlena.c`). goopg's `Column.Compression`
is dump-fidelity metadata only — no code path ever actually compresses a
stored value with it — so there is no per-datum compression-method state
for the function to read. This appears roughly twenty times across the
file (every `SELECT pg_column_compression(...)` call) and is REFACTOR-tier:
it needs a new per-datum storage concept, not just a new builtin
registration.

Three further independent buckets, confirmed unrelated to the compression
clause itself:

1. **Multi-inheritance compression-method conflict** — `CREATE TABLE
   cminh() INHERITS(cmdata, cmdata1)` should raise `column "f1" has a
   compression method conflict` (`DETAIL: pglz versus lz4`) when two parent
   tables declare different non-default methods for the same merged
   column; goopg's INHERITS column-merge path has no such check.
2. **Materialized-view `SET COMPRESSION` view-definition propagation** —
   `ALTER MATERIALIZED VIEW ... ALTER COLUMN x SET COMPRESSION lz4`
   doesn't affect `\d+`'s `View definition:` rendering, which shows the raw
   `SELECT * FROM cmdata1` query text rather than PG's column-substituted
   `SELECT f1 AS x FROM cmdata1` — looks like a pre-existing MV
   column-aliasing deparse gap, not compression-specific; unconfirmed,
   needs its own sizing pass.
3. **`fipshash()` output length drifts by one byte** from PG's real
   FIPS-186 hash on a `large_val()`-derived string (`length(f1)` 12449 vs
   12450) — a pre-existing hash-implementation fidelity gap surfaced by
   this file's externally-stored-value test, not a compression bug.

## References

- `postgres/src/backend/commands/tablecmds.c:22043-22076`
  (`GetAttributeCompression` — the exact rule ported here), call sites at
  `:1446` (`DefineRelation`) and `:18781` (`ATExecSetCompression`).
- `internal/executor/operators_ddl.go` — `validateColumnCompression`
  (new), wired into `execCreateTable`'s `addCol` closure, the fallback
  column loop, and `AlterTableSetCompression`.
- `internal/parser/ddl.go` — `normalizeCompressionMethod` (no longer
  discards unrecognized method names).
- `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0105.
