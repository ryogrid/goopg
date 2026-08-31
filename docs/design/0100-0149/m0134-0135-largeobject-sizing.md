# M0134-0135 — `largeobject.sql` sizing (PARKED, no CONTAINED bucket)

Status: sizing only, no code shipped this loop. Matches the M0134-0038
(`json.sql`) precedent — a `not-tried` case that, once run, turns out to be
dominated entirely by a missing subsystem rather than a local bug.

## Sizing result

`scripts/pg-regress-runner.sh --verbose largeobject`: 0/1 PASS, 0% parity.
Every one of the file's 40+ large-object calls fails with `function ... does
not exist` (`lo_create`, `lo_creat`, `lo_open`, `lo_close`, `lo_unlink`,
`loread`/`lowrite`, `lo_lseek[64]`, `lo_tell[64]`, `lo_truncate[64]`,
`lo_import`/`lo_export`, `lo_from_bytea`, `lo_get[_fragment]`, `lo_put`) or a
downstream cascade of the first such error aborting the surrounding
transaction.

## Root cause

Large Object catalog OIDs are seeded in `pg_proc`
(`internal/initdb/pg_proc_seed_data.go:407-2316`, `HandlerName: "be_lo_*"`)
but **no dispatcher in `internal/executor` ever recognizes a `be_lo_*`
handler name** — `evalFuncCall` falls through to "function does not exist"
for all of them. This mirrors PG's real large-object facility
(`postgres/src/backend/libpq/be-fsstubs.c`) which is missing in its
entirety:

- `pg_largeobject` / `pg_largeobject_metadata` catalog storage (PG chunks
  object data into `LOBLKSIZE`-sized rows keyed by `(loid, pageno)`).
- A per-backend, per-transaction large-object descriptor table
  (`cookies[]` in `be-fsstubs.c`) backing `lo_open`/`lo_close`/`lo_lseek`/
  `lo_tell`/`loread`/`lowrite`/`lo_truncate`, all of which are stateful
  across statements within one transaction (`UPDATE ... SET fd =
  lo_open(...)` then reusing `fd` in later `SELECT`s in the SAME
  transaction is the file's dominant idiom).
- Whole-object convenience functions (`lo_from_bytea`, `lo_get[_fragment]`,
  `lo_put`) that operate on `pg_largeobject` directly without a descriptor.
- `\lo_import`/`\lo_export`/`\lo_list`/`\lo_unlink`/`\dl` psql meta-commands
  and `:LASTOID` psql variable support.
- `ALTER LARGE OBJECT ... OWNER TO`, `GRANT ... ON LARGE OBJECT`,
  `COMMENT ON LARGE OBJECT` — DDL forms distinct from the regular
  relation-ACL/comment paths (large objects are not relations).
- Read-only-transaction write rejection specific to large objects
  (`be_lo_open` in `INV_WRITE` mode, `lo_creat`, `lo_unlink`, etc. must all
  raise `ERRCODE_READ_ONLY_SQL_TRANSACTION` — none of goopg's read-only
  guards know about large objects since the functions don't exist to reach
  them).

None of these pieces exist today. This is a full, self-contained storage
subsystem (own catalog tables, own chunking/paging format, own
transaction-scoped session state) — REFACTOR-tier, not a case-local fix.

## Secondary finding (already ledgered, not new)

`SELECT lo_open(2121, x'40000'::int)` additionally exposes the pre-existing,
already-ledgered M0134-0092 gap: `x'...'` hex bit-string literals decode to a
plain untyped `StringConst` holding binary-digit text
(`internal/parser/expr.go` `decodeBitStringLit`), not a BITOID-typed value,
so `::int` reaches `evalCast`'s `KindString` branch and tries to parse the
digit text as **decimal** (`parseIntegerInput`) instead of dispatching PG's
`bittoint4` (`postgres/src/backend/utils/adt/varbit.c:1585-1607`, raw-bits
reinterpretation with end-padding shift) — producing "value
\"01000000000000000000\" is out of range for type integer" instead of
`262144`. No safe file-local fix exists: a bit-literal-derived StringConst is
indistinguishable at the Datum level from a real string literal with the same
digit text, so the real fix is giving bit-string literals a proper BITOID
type (the M0134-0092 resume point), not a special case in `evalCast`.

## Verdict

PARKED. CSV row flipped `not-tried` → `failed` (case genuinely fails, not a
stale status) via `make regen-testport`, `pass_required` stays `no`. No
re-arm trigger beyond "large object subsystem exists" — this is a strong
candidate for its own milestone (own catalog tables + per-txn descriptor
table + psql meta-command support), sized here for future scoping.

See `.ralph/deferral_ledger.md` (2026-08-24, M0134-0135) for the resume
point.
