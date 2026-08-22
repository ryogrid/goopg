# M0134-0072 — `temp.sql`: `ON COMMIT` end-of-transaction semantics

Status: accepted · Date: 2026-08-22 · Milestone: M0134-0072

## The case

`temp.sql` is a `failed` regress row (CSV
`docs/test-port/postgres-oracle-target-inventory.csv:198`). Sized at HEAD
(`11da963b`) it is **507 diff lines / 11 hunks / 37 `^+ERROR`, deterministic
across two fresh-cluster runs** — genuinely failing, not stale (unlike
`groupingsets`/`subselect`, `temp` is a trustworthy sentinel).

The diff splits into ten root-cause buckets. Two are milestone-scale and out of
scope for any slice (`DECLARE CURSOR`/portal subsystem — 34 lines in one hunk;
domain-CHECK enforcement — 10 lines). The largest *contained* slice is the
`ON COMMIT` feature, which spans 9 of 11 hunks (~69 lines) and is genuinely
missing, not diverging.

## Root cause: `ON COMMIT` is parsed and discarded

`temp.sql` exercises the full temp-table lifecycle — `CREATE TEMP TABLE … ON
COMMIT DELETE ROWS` / `ON COMMIT DROP`, temp views/sequences, and the
per-session name-shadowing of permanent tables. goopg parses the `ON COMMIT`
clause but never records or executes it:

- `internal/parser/ddl.go:3752-3759` (plain `CREATE TABLE` arm) and
  `:3460-3463` (partition-child arm) both consume the clause as
  `_ = p.acceptIdentKeyword(...)` — a discard, not a capture.
- `CreateTableStmt` (`internal/parser/ast.go:1392-1543`) has **no `OnCommit`
  field** at all.
- There is no commit-time hook anywhere in `internal/executor/operators_tx.go`,
  so even a captured value would never fire.

PG's shape (`./postgres/src/backend/`):

- `DefineRelation` records the action via `register_on_commit_action`
  (`commands/tablecmds.c:19261`), keyed on the current subtransaction.
- `PreCommit_on_commit_actions` (`tablecmds.c:19320`) is the commit-time pass —
  called from `CommitTransaction` (`access/transam/xact.c:2311`), **before**
  `PreCommit_CheckForSerializationFailure` (`:2339`) and
  `RecordTransactionCommit` (`:2365`). `DELETE ROWS` truncates the heap
  (`ExecOnCommitActions` → `heap_truncate`); `DROP` drops the temp relation.
- Non-temp `CREATE TABLE … ON COMMIT …` raises 42P16
  `ON COMMIT can only be used on temporary tables`
  (`tablecmds.c:799-803`).
- The truncate-vs-FK interaction has a temp-specific message: `heap_truncate_check_FKs`
  (`access/heap/heap.c:3738`) raises `unsupported ON COMMIT and foreign key
  combination` + DETAIL (0A000) for a temp table referenced by a FK, instead of
  the generic `cannot truncate a table referenced in a foreign key constraint`.
- Grammar: `create_as_target: qualified_name opt_column_list OptOnCommit
  OPT_TABLESPACE` (`gram.y`) — the `OptOnCommit` sits **between** the optional
  column-alias list and `AS`, which goopg's CTAS lookahead does not admit.

## The slice (Bucket A, folding Bucket B's lookahead)

One feature, four bounded facets, all additive. Nothing here touches the
temp-shadow catalog keying, cursor portals, or domain coercion — those stay
open and are recorded separately.

1. **Parser — capture `OnCommit`** (sibling pair, both must change together):
   - Add an `OnCommit` field to `CreateTableStmt` (a small enum/string:
     `""`/`DELETE ROWS`/`DROP`; `PRESERVE ROWS` is the default no-op).
   - Capture it at the plain arm (`ddl.go:3752-3759`) **and** the
     partition-child arm (`:3460-3463`).
   - Extend the CTAS alias-list lookahead (`ddl.go:3031-3061`, esp. `:3052`) so
     the `(col)` list accepts an optional `ON COMMIT …` between `)` and `AS`.
     This must NOT regress the immediate-`AS` form (`CREATE TEMP TABLE t(col) AS
     SELECT`, `ColumnAliases`, M0097-0020) — the lookahead still succeeds when
     `)` is followed directly by `AS`.
2. **Guard — 42P16 on non-temp**: `CREATE TABLE … ON COMMIT …` without `TEMP`
   raises `ON COMMIT can only be used on temporary tables` (mirror
   `tablecmds.c:799-803`), both plain and CTAS paths.
3. **Commit hook — run the actions**: at `CREATE TEMP TABLE` register the
   (OID, action) pair in a per-session list keyed to the current (sub)transaction
   (mirror `register_on_commit_action`, `tablecmds.c:19261`); in
   `transactionOp.execCommit` (`internal/executor/operators_tx.go:117`), run the
   pass **before** `CommitTransaction` (`:192`) and **before** the SSI check
   (`:161`), mirroring `xact.c:2311 → :2339`. `DELETE ROWS` reuses the existing
   `execTruncate` (`operators_ddl.go:15454`); `DROP` reuses the catalog
   drop/cascade path. Partition/inheritance variants (hunks 6–9) fall out of the
   same hook with no new mechanism. On `ROLLBACK` the list is simply abandoned —
   PG's `AtAbort` has no ON-COMMIT pass either, so `ON COMMIT` never fires on
   abort.
4. **FK-compat message**: route the ON-COMMIT truncate through the existing
   `execTruncate` FK scan (`operators_ddl.go:15549`) and add the temp
   special-case — `unsupported ON COMMIT and foreign key combination` + DETAIL
   (0A000) — mirroring `heap.c:3738`'s `tempTables` branch, so a temp table
   referenced by a FK gets PG's exact message rather than the generic truncate
   error.

## Sibling paths (brief BOTH)

- ON COMMIT parser arm: plain (`ddl.go:3752`) ↔ partition-child (`:3460`).
- ON COMMIT hook: `execCommit` ↔ `execRollback` (rollback is a deliberate no-op,
  matching `xact.c AtAbort`; document it).
- CTAS lookahead: the `(col)` list is *also* the successful immediate-`AS` form —
  the fix must keep both parse paths live.

## Honest expectation

Bucket A does **not** flip `temp.sql` to PASS. It closes the `ON COMMIT` wall
and the CTAS-alias hunks (~85 of 196 changed lines, 7 full hunks + 2 partial),
leaving the independent residuals open: Bucket C (PREPARE TRANSACTION
temp-object guard, ~17 lines — the natural *next* tiny slice), Bucket D
(temp-shadow schema-qualified keying + permanent-index loss, ~13), Bucket E
(`current_schema()` ignores `pg_temp`, ~2), Bucket F (`temp_buffers` GUC, ~1),
and the two milestone-scale buckets G (cursors) and J (domain CHECK). CSV row
stays `failed`/`pass_required=no` unless the case reaches byte-parity; no
`make regen-testport`.

## Recorded for later loops (not this slice)

- **C — PREPARE TRANSACTION temp guard**: `execPrepareTransaction`
  (`internal/postmaster/twophase.go:124`) lacks the 0A000
  `cannot PREPARE a transaction that has operated on temporary tables` check;
  signal already exists (`EnsureTempNamespace`, `catalog.go:15827`;
  `sessionTempOwner`, `context.go:1866`). Mirror `xact.c:2613-2617`
  (`XACT_FLAGS_ACCESSEDTEMPNAMESPACE`).
- **D — temp shadow**: `execCreateTable` shadow path (`operators_ddl.go:1817-1834`)
  drops the permanent table via the unqualified `Catalog.DropTable(s.Name)` and
  omits its indexes on restore.
- **G — DECLARE CURSOR / portal subsystem** (milestone-scale).
- **J — domain CHECK in casts + function-syntax domain coercion** (domain
  subsystem; the arg-list/HINT half is the `equivclass` builtin-name-switch class).
