# M0134-0157 — `psql_pipeline.sql`: the extended protocol has no implicit transaction block

Status: **accepted** (sizing + one contained fix landed; the dominant cause is
PARKED as REFACTOR-tier with the design below as its resume point)

Upstream case: `postgres/src/test/regress/sql/psql_pipeline.sql`
(442 lines SQL / 768 lines expected output), CSV row `not-tried` → `failed`.

## What the case actually exercises

Despite living behind seven psql backslash commands (`\startpipeline`,
`\bind`, `\sendpipeline`, `\syncpipeline`, `\flushrequest`, `\getresults`,
`\endpipeline`), essentially none of the divergence is client-side. The file is
a **server-side extended-query-protocol test**: it drives Parse/Bind/Describe/
Execute/Flush/Sync message groups and asserts what the *backend* does when more
than one Execute precedes a Sync.

Measured at HEAD before this loop: **357 diff lines, 22 `^+ERROR`, 12
`^-ERROR`** (`scripts/pg-regress-runner.sh --verbose psql_pipeline`).

Bucketing that diff yields exactly **two** independent root causes — unusually
few for an M0134 case, because one of them cascades over ~85% of the file.

## Cause 1 (dominant, PARKED) — no implicit transaction block

### What PostgreSQL does

The extended protocol does **not** commit at Execute. `exec_execute_message`
finishes a completed portal with either

* `finish_xact_command()` — only when the statement demanded immediate commit
  (`PreventInTransactionBlock`-class commands), or
* `CommandCounterIncrement()` **and `MyXactFlags |= XACT_FLAGS_PIPELINING`**
  (`postgres/src/backend/tcop/postgres.c:2306-2323`), leaving the transaction
  open.

The commit therefore happens at Sync:

```c
case PqMsg_Sync:
    pq_getmsgend(&input_message);
    EndImplicitTransactionBlock();      /* postgres.c:4968 */
    finish_xact_command();
    send_ready_for_query = true;
```

and the *block state* is raised one message later than the transaction:

```c
static void start_xact_command(void)
{
    if (!xact_started) { StartTransactionCommand(); xact_started = true; }
    else if (MyXactFlags & XACT_FLAGS_PIPELINING)
        BeginImplicitTransactionBlock();   /* postgres.c:2794-2803 */
```

`BeginImplicitTransactionBlock` (`access/transam/xact.c:4326`) flips
`TBLOCK_STARTED → TBLOCK_IMPLICIT_INPROGRESS`; `EndImplicitTransactionBlock`
(`xact.c:4351`) flips it back so the pending `CommitTransactionCommand` commits
"whatever happened during the implicit transaction block as though it were a
single statement".

Three consequences the case asserts, all of which goopg gets wrong:

1. **The first command in a message group is NOT in a transaction block**, but
   every later one is. Hence upstream expects
   `REINDEX TABLE CONCURRENTLY` to *succeed* as the first command of a pipeline
   and to fail with `REINDEX CONCURRENTLY cannot run inside a transaction
   block` as the second; `LOCK psql_pipeline` to *fail* with `LOCK TABLE can
   only be used in transaction blocks` as the first and succeed as the second;
   `SET LOCAL` to emit `WARNING: SET LOCAL can only be used in transaction
   blocks` as the first and take effect silently as the second; and `VACUUM` to
   fail with `VACUUM cannot run inside a transaction block` when it follows
   another command but succeed after an intervening `\syncpipeline`.
2. **A mid-group `BEGIN` converts the implicit block into an explicit one**
   (`BeginTransactionBlock`, `TBLOCK_IMPLICIT_INPROGRESS → TBLOCK_BEGIN`) on
   the *same* transaction — so a later `ROLLBACK` undoes statements that were
   sent *before* the `BEGIN`.
3. Sending Sync commits; the next message group starts fresh.

### What goopg does

`executeExtendedQueryViaExecutor` (`internal/postmaster/dispatch_extended.go:171-201`)
is auto-commit-per-Execute:

```go
inBlock := connTx != nil && connTx.InExplicit()
if inBlock { tx = connTx.Tx() } else { tx, err = s.cfg.TxnMgr.Begin(...) }
ownTx := !inBlock
```

and `MsgSync` (`internal/postmaster/server.go:1820-1834`) only clears
`syncRequired`, delivers notifications, and writes ReadyForQuery — it never
touches `connTx`. There is no representation of `TBLOCK_IMPLICIT_INPROGRESS`
anywhere: `connTxState` has `active` (explicit block) and `failed`, nothing
between.

### How the cascade forms

`psql_pipeline.sql:91-96` is

```
INSERT INTO psql_pipeline VALUES (1)   -- implicit block: not committed yet
BEGIN                                  -- converts the implicit block to explicit
INSERT INTO psql_pipeline VALUES (2)
ROLLBACK                               -- undoes BOTH inserts
```

Upstream leaves the table empty. goopg commits the first INSERT on its own, so
row `a=1` survives. The very next pipeline (`:99-106`,
`BEGIN / INSERT 1 / ROLLBACK / BEGIN / INSERT 1 / COMMIT`) then raises
`duplicate key value violates unique constraint "psql_pipeline_pkey"` inside an
*explicit* block. Because an errored message group correctly skips every later
message until Sync, that block's `COMMIT` is never executed — and since an
explicit block legitimately survives Sync in aborted state (this part goopg
gets right, and matches PG), the session stays aborted for the **remaining 300
lines of the file**, which is why the diff is a wall of
`current transaction is aborted, commands ignored until end of transaction
block`.

So a single missing feature accounts for the entire tail. The error-count
metrics are therefore not a useful progress signal for this case until the
feature lands.

### Sketch of the fix (resume point)

goopg already implements the *simple*-path analogue: a multi-statement Query
message runs under one transaction (`dispatchSimpleQueryViaExecutor` begins
exactly one `mvcc.Transaction` for the whole message; guarded by
`TestSimpleQueryBatchAbortUndoesEarlierCreateTable`), and a mid-batch `BEGIN`
promotes it — see `internal/postmaster/dispatch.go:1071` ("execBegin's promotion
of the implicit RC tx to an explicit RR tx"). The extended path needs the same
thing, spanning *messages* instead of statements:

1. Add a message-group transaction holder to `extendedState` (the struct is
   already per-connection and already reset around Sync via `syncRequired`),
   plus an `implicitBlock bool` on `connTxState` distinguishing
   `TBLOCK_STARTED` from `TBLOCK_IMPLICIT_INPROGRESS`.
2. In `executeExtendedQueryViaExecutor`, replace `ownTx := !inBlock` with:
   join the explicit block if one is active; else adopt the message group's
   open transaction (promoting `connTx` into an implicit block, so
   `ectx.InExplicitTransaction()` becomes true from the *second* Execute
   onwards); else begin one and hand it to the message group instead of
   committing it.
3. Port PG's immediate-commit list (`PreventInTransactionBlock` callers:
   VACUUM, CREATE/DROP DATABASE, CREATE INDEX/REINDEX CONCURRENTLY, CREATE
   TABLESPACE, …) so those still commit inside Execute and do **not** arm the
   pipelining flag — goopg already has the in-block rejection logic these need
   (`dispatch_extended.go:333` "can only be used in transaction blocks").
4. `MsgSync`: end the implicit block (commit, or roll back when failed) before
   `deliverNotifications` + ReadyForQuery. Connection teardown must roll back a
   still-open message-group transaction.
5. `BEGIN` arriving while the implicit block is in progress must **not** begin a
   second transaction — `applyTransactionVerb` has to convert in place, exactly
   as the simple path's `execBegin` promotion does.

This is a transaction-lifecycle change affecting every extended-protocol
client, so it needs the full gate set (units precommit + a wide regress sweep +
pgbench smoke + TPC-H spot-check + the TPC-DS SF0.5 gate). It was **not**
attempted in this loop precisely because the SF0.5 gate hard-refuses while the
nightly CI batch is live, and shipping a lifecycle change without it would be
reckless.

## Cause 2 (CONTAINED — landed this loop) — Bind/Describe message-text parity

Seven of the diff's lines were goopg paraphrases of upstream's user-visible
protocol-violation texts. `internal/postmaster/extended.go` now emits the
upstream strings verbatim:

| site | goopg before | upstream (`postgres.c`) |
|---|---|---|
| Bind, wrong parameter count | `bind supplies 0 parameters, prepared statement requires 1` | `bind message supplies 0 parameters, but prepared statement "" requires 1` (`:1729`) |
| Bind, wrong parameter-format count | `invalid Bind message: parameter format code count mismatch` | `bind message has %d parameter formats but %d parameters` (`:1723`) |
| Bind / Describe `S`, unnamed statement missing | `prepared statement "" does not exist` | `unnamed prepared statement does not exist` (`:1671`, `:2669`) |

The last row is the one with semantic content rather than wording: upstream
consults the prepared-statement table only for a **non-empty** name and lets the
empty name fall through to `unnamed_stmt_psrc == NULL`, so the message carries
no name at all. Bind and Describe are sibling paths over the same lookup (they
had already diverged in phrasing once), so both now route through one helper,
`missingPreparedStatement`. SQLSTATEs were already correct
(`ERRCODE_PROTOCOL_VIOLATION` / `ERRCODE_UNDEFINED_PSTATEMENT`) and are
unchanged; the parameter-format predicate is likewise unchanged (goopg's
`!=0 && !=1 && != nParams` is upstream's `numPFormats > 1 && numPFormats !=
numParams` spelled out).

Guard: `internal/postmaster/extended_bind_message_parity_test.go`
(`TestExtendedBindMessageTextMatchesUpstream`, 8 subtests covering named/unnamed
× too-few/too-many parameters, format-count mismatch, and missing-statement Bind
*and* Describe).

Result: **357 → 291 diff lines (-66)**, `^+ERROR` 22 → 15. The case stays
`failed`/PARKED on Cause 1.

## Also observed (ledgered, not fixed)

`handleBindFrame` rejects any Bind carrying more than one **result** format code
with `invalid Bind message: result format code count mismatch`. Upstream accepts
`nFormats == natts` (one code per output column) and only errors when the count
matches neither 0, 1, nor the column count — `bind message has %d result formats
but query has %d columns`, `postgres/src/backend/tcop/pquery.c:642`. goopg
supports text output only, so a client sending per-column format codes gets a
spurious `08P01` today. Not exercised by `psql_pipeline.sql`; recorded in the
deferral ledger.
