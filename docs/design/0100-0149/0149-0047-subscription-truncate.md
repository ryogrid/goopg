# M0149\-0047 — Port subscription/t/010\_truncate.pl

Status: in progress. TestPort\_Subscription010Truncate is written \(three pairings\) and gated by GOOPG\_TAP\_010. The PG
18.3 publisher with a goopg subscriber passes all 15 assertions. Both goopg\\-publisher pairings fail because a goopg
publisher writes no TRUNCATE to the logical stream \(M0149\-0047b\).

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

## Open — M0149\-0047b, TRUNCATE from a goopg publisher

goopg's TRUNCATE writes no XLOG\_HEAP\_TRUNCATE record for logical decoding, so the slot decoder sees nothing. PG writes
xl\_heap\_truncate with the relids and the CASCADE / RESTART\_SEQS flags at wal\_level = logical \(ExecuteTruncateGuts →
XLogInsert\). The decoder turns it into a REORDER\_BUFFER\_CHANGE\_TRUNCATE, and pgoutput\_truncate sends `T` for the
published relids whose publications carry pubtruncate.
