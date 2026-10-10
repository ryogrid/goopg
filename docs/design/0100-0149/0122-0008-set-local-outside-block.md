# M0122-0008 — SET LOCAL outside a transaction block, and AtEOXact_GUC for autocommit transactions

Status: landed 2026-10-10 (1d10d426b). Parent: M0122-0008.

## Problem

The session GUC registry (`misc.SessionRegistry`) dropped its local layer
only when an explicit transaction ended (the executor's BEGIN/COMMIT hooks).
Outside BEGIN, that meant:

- **SET LOCAL leaked.** `SET LOCAL work_mem = '5MB'` set work_mem for the
  rest of the session, with no warning. `set_config(..., true)` and
  `SET LOCAL ROLE` / `SET LOCAL SESSION AUTHORIZATION` leaked the same way.
- **Aborted messages kept their SETs.** In `SET work_mem = '7MB' \; SELECT
  1/0` the message aborts, but goopg kept 7MB.
- **No implicit block.** In PG a message of several statements runs in an
  implicit transaction block: SET LOCAL there applies until the message
  ends, without a warning. goopg had no notion of that block.

## PG behaviour

- **The SET LOCAL warning.** `ExecSetVariableStmt` (guc_funcs.c) calls
  `WarnNoTransactionBlock(isTopLevel, "SET LOCAL")`. Outside
  `IsTransactionBlock()` that is the 25P01 WARNING "SET LOCAL can only be
  used in transaction blocks" (xact.c `CheckTransactionBlock`). The value
  is still applied.
- **Where it ends.** The value ends with the surrounding transaction:
  `AtEOXact_GUC` pops `GUC_LOCAL` entries at commit and undoes everything at
  abort. A lone autocommit statement is its own transaction, so a lone SET
  LOCAL is observable only through its warning, or through an error from
  validating its value.
- **Implicit blocks.** `exec_simple_query` runs a message of more than one
  statement in an implicit block (`use_implicit_block`, state
  `TBLOCK_IMPLICIT_INPROGRESS`). That counts as a block, so SET LOCAL there
  draws no warning and lasts until the message ends. A BEGIN inside the
  message promotes the same transaction into an explicit block.
- **`set_config(name, value, true)`** never warns. It is local to the current
  transaction.

## Change

### `misc.SessionRegistry`

- **`BeginImplicitTransaction(implicitBlock)` /
  `EndImplicitTransaction(committed)`** are the GUC side of an autocommit
  transaction. While one is open, `snapshotPrior` journals plain SETs (as
  it does inside BEGIN). The end runs the shared `endXact`:
  - drop the local layer;
  - on abort, restore the journal.
- **BEGIN promotion.** `BeginTransaction` keeps the journal when it promotes
  an open autocommit transaction. If that promoted block is still open when
  the message ends, `EndImplicitTransaction` leaves it alone; its own
  COMMIT or ROLLBACK ends it.
- **`InTransactionBlock()`** is `IsTransactionBlock()`: an explicit block,
  or an implicit one.
- **`CheckSet(name, value)`** validates exactly as `Set` does (the two share
  `prepareSet`) without storing anything.

### The two dispatchers

- **`dispatchSimpleQueryViaExecutor`** opens the GUC transaction for an
  autocommit message, marked as an implicit block when the message holds more
  than one statement.
- **`executeExtendedQueryViaExecutor`** opens one for an Execute that owns
  its transaction.
- **Where they end it.** On commit it ends before ReadyForQuery, so any
  ParameterStatus precedes it; on abort it ends in the existing rollback
  `defer`.

### SET LOCAL sites

Each raises the 25P01 warning outside a block:

- **Executor `SetStmt`,** through the new `Context.InTransactionBlock`.
- **The simple and extended SET LOCAL fast paths.** These are always a lone
  statement outside a block, so they warn and run `CheckSet` only. The net
  effect is the same as PG's (apply, then discard at commit), without
  emitting a spurious pair of ParameterStatus messages.
- **SET LOCAL ROLE / SESSION AUTHORIZATION fast paths.** They warn and leave
  the identity unchanged.

## Verification

- **Live against a scratch PG 18.3,** on one psql session covering simple
  and multi-statement messages and `\bind` (extended): every SHOW value and
  every WARNING matches. The covered cases:
  - lone SET LOCAL, valid and invalid value;
  - SET LOCAL in an implicit block;
  - SET LOCAL TIME ZONE / ROLE / SESSION AUTHORIZATION / DateStyle;
  - an aborted message's plain SET;
  - BEGIN in the same message after SET LOCAL;
  - `set_config(..., true)`;
  - extended SET LOCAL.

  The only differences left are environmental (initdb defaults) and the open
  range-error wording task.
- **Tests.**
  - `TestSessionImplicitTransaction` (misc): drop, abort revert, promotion,
    block predicate, `CheckSet`.
  - `TestPort_SetLocalOutsideTransactionBlock`: one psql session, PG's
    SHOW sequence and exactly two warnings.
- **Gates.**
  - units, 22 GUC/role/transaction/extended testport tests, and the
    isolation family (122 PASS);
  - regress A/B: `guc` lost its whole SET LOCAL hunk; the standard set is
    identical; `plpgsql` is a confirmed flap (HEAD and new reruns are
    byte-identical); `rowsecurity` differs only by pointer noise;
  - TPC-H spotcheck and acceptance arm;
  - fire set (no fires at either scale) and SF0.25 sweep (PASS=99).

## Not covered (ledgered)

- **An extended Execute is always its own GUC transaction.** In PG, Executes
  before one Sync share a transaction, so a `set_config(..., true)` value
  would survive to the next Execute of the same Sync.
- **SET LOCAL ROLE / SESSION AUTHORIZATION in an implicit block** (through
  the executor) still restore through connTx's explicit-block snapshot.
  Whether the identity ends with the message is unverified.
- **No role validation outside a block.** A lone SET LOCAL ROLE does not
  check that the role exists. PG warns and then reports `role "x" does not
  exist`.
- **Extended errors lose the warning.** The extended fast path returns the
  validation error without the warning that should precede it.
- **No placeholder registration.** `CheckSet` does not register a
  placeholder for an unknown custom name. PG's `find_option` creates one
  that outlives the discarded value.
