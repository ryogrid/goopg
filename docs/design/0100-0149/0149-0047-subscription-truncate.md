# M0149\-0047 — Port subscription/t/010\_truncate.pl

Status: done. TestPort\_Subscription010Truncate passes 3/3 in all three pairings \(all\-goopg, PG 18.3 publisher →
goopg subscriber, goopg publisher → PG 18.3 subscriber\).

## Landed — M0149\-0047a, a goopg subscriber restarts sequences on a replicated RESTART IDENTITY

PG's apply\_handle\_truncate passes the Truncate message's TRUNCATE\_RESTART\_SEQS option bit \(2\) to
ExecuteTruncateGuts. That resets every sequence the table owns \(getOwnedSequences\). goopg's apply worker ignored the
option byte.

goopg's own `TRUNCATE … RESTART IDENTITY` also missed a sequence attached with `ALTER SEQUENCE s OWNED BY t.c` when the
column has no `nextval()` default. It found sequences only by the serial / identity naming convention and through a
column's nextval default.

`restartTruncatedSequences` is now shared by the TRUNCATE statement and the apply worker. It resets:

- the serial / identity sequence;
- the nextval\\-default sequence;
- the sequence OWNED BY each column \(FindSequenceOwnedBy\).

The apply worker calls it when the option bit is set. It always truncates RESTRICT, as PG's does, so the CASCADE bit
\(1\) needs no handling. Unit test TestRestartTruncatedSequencesOwnedBy.

## Landed — M0149\-0047b, a goopg publisher replicates TRUNCATE

- **WAL.** execTruncate collects every logically logged relation it truncates \(RelationIsLogicallyLogged: permanent,
  not foreign, a user relation; a partitioned parent counts, as in ExecuteTruncate's relids\_logged\). At
  wal\_level = logical it then writes one XLOG\_HEAP\_TRUNCATE record \(`logHeapTruncate`,
  `xlog.EncodeHeapTruncatePG`\). The record carries xl\_heap\_truncate \(dbId, nrelids, the XLH\_TRUNCATE\_CASCADE /
  RESTART\_SEQS flags, padding to 12 bytes, the relid array\) as main data only. Recovery treats it as a no\-op, as
  heap\_redo does.
- **Decoding.** classifyDecodedXLog decodes the record before its block\-0 lookup \(it has no block references\). It
  skips a TRUNCATE from another database \(DecodeTruncate's dbId check, the same filter as logical messages\). Otherwise
  it queues one `ChangeTruncate` carrying the relids and flags in the truncating transaction.
- **Output.** `PgOutput.truncate` is pgoutput\_truncate. Of the truncated relations, it keeps those that are
  publishable and whose publications carry publish = 'truncate' \(the publication filter's new truncate flag\). It sends
  each kept relation's Relation message if not yet sent, then one `T` with the option bits. Nothing is sent when no
  relation qualifies.
- **Publication flag.** Publications now model pubtruncate \(`Publication.PublishTruncate`\). It defaults on, parses
  from `publish`, is copied by ALTER PUBLICATION SET, and is persisted in and reloaded from the pg\_publication heap
  \(attnum 8\). pg\_publication shows it, so `\dRp` and pg\_dump now print PG's `Truncates = t` and
  `publish = 'insert, update, delete, truncate'`.

Tests: TestHeapTruncateCodec and TestHeapTruncateDecodesToPgoutputTruncate \(internal/access/transam/xlog\).
TestPort\_Subscription010Truncate is ungated and passes 3/3 in all three pairings.
