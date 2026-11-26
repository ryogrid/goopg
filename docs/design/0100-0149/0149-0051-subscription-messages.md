# M0149\-0051 — Port subscription/t/020\_messages.pl

Status: done. TestPort\_Subscription020Messages passes 3/3 in all three pairings \(all\-goopg; PG 18.3 publisher →
goopg subscriber; goopg publisher → PG 18.3 subscriber\).

020 drives a subscription, disables it, and then reads the subscription's slot through the SQL decoding interface:
`pg_logical_slot_peek_binary_changes` / `pg_logical_slot_get_binary_changes` with pgoutput and `messages 'true'`. It
checks the `M` message bytes, transactional vs non\-transactional messages \(xid 0\), that a message in an aborted
transaction still decodes when non\-transactional, and that get advances the slot while peek does not. The PG 18.3
publisher pairing passed as soon as the test was written. The goopg\-publisher pairings needed 0051a.

## Landed — M0149\-0051a, the SQL slot\-change functions

PG: logicalfuncs.c `pg_logical_slot_get_changes_guts`. It creates a decoding context on the slot, decodes from the
slot's restart point to `GetFlushRecPtr()`, and returns one row per `OutputPluginWrite` — `lsn` \(the callback's
`write_location`\), `xid`, `data`. Only the get form confirms the slot.

goopg:

- `replication.Handler.SQLSlotChanges` \(internal/replication/sql\_slot\_changes.go\) checks the slot \(exists, logical,
  pgoutput, not active; even\-length options\), builds the catalog snapshot and a PgOutput with the live publication
  filter and the `messages` option, and runs a `RecordIterator` from the slot's restart point. The end is
  `WrittenLSN()` at call time; the loop stops once the new `RecordIterator.ConsumedThrough` reaches it.
  `AtEndOfWAL` cannot serve as the stop test because the parked flag is cleared when `Next` returns — the first
  version hung on exactly that.
- `sqlChangeCollector` turns each pgoutput write into a row. It applies pgoutput's empty\-transaction rule
  \(`pgoutput_send_begin` is lazy and `pgoutput_commit_txn` skips a transaction that sent nothing\): a `B` is held
  until something else is written, and `B` directly followed by `C` is dropped.
- The SRFs are generic builtins \(catalog `builtinSRFColumns`, optimizer `builtinSRFSpecs` with variadic options,
  executor `builtinSRFFuncs`\). They are not strict, because PG's `upto_lsn` / `upto_nchanges` are NULL in the
  common call; `builtinSRFNotStrict` exempts them from the NULL\-argument skip. A NULL slot name is 22004.
- `ExecContext.LogicalSlotChanges` is wired in both the simple and the extended dispatch paths.
- The analyzer accepts `xid = int` and `xid <> int` \(xideqint4 / xidneqint4; no ordering operators\), which 020 uses
  as `WHERE ... AND xid = 0`.

Tests: TestSQLChangeCollectorEmptyTransaction, TestXidIntEquality, TestPort\_Subscription020Messages.

## Open residuals

- M0149\-0051b: a `B` row should carry `txn->first_lsn` \(goopg's reorder buffer keeps no first LSN, so it shows the
  commit LSN\); `upto_lsn` / `upto_nchanges` are accepted but ignored; only pgoutput is supported \(not
  test\_decoding, not the text\-form `pg_logical_slot_get_changes`\).
- M0149\-0051c: a slot made by `CREATE_REPLICATION_SLOT … LOGICAL pgoutput` has an empty plugin and database in
  `pg_replication_slots` and in its state file, so after a restart it loads as physical.
