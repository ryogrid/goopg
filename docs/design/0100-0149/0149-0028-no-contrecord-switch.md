# M0149\-0028 — Port recovery/t/043\_no\_contrecord\_switch.pl

Status: done — TestPort\_Recovery043NoContrecordSwitch passes on all three pairings \(M0149\-0028, M0149\-0028a\).

## What the test needs

The primary's last record crosses a WAL page boundary, and the page it continues onto is zeroed in the archived
segment. Two standbys restore from the archive only, with no `primary_conninfo`. Both must stop at that page with PG's
message `invalid magic number 0000 in WAL segment <seg>, LSN <page>, offset <off>`. standby1 is promoted. standby2 gets
`primary_conninfo` by a reload, streams from standby1, follows timeline 2 from the torn record's start, and must replay
1000 rows.

Path\-to\-TAP: 043 ← \(1\) an invalid record is the end of the available WAL on a standby ← \(2\) a reload that sets
`primary_conninfo` switches an archive\-only standby to streaming ← \(3\) M0149\-0028a, pg18 pairing only.

## Landed

- Continuation page validation \(`RecordIterator.readRecordBytesAt` → `validateContPageHeader`\). Each page a record
  continues onto gets XLogReaderValidatePageHeader's magic, info\-bits and pageaddr checks once its header bytes exist.
  A failure is `invalidPageHeaderError`, carrying PG's text. This applies to every reader: a goopg primary's own WAL
  always has valid headers, and unwritten bytes are never read.
- `RecordIterator.SetEndOfWALAtInvalidRecord` \(set by standby replay\). An invalid continuation page or a CRC
  mismatch is ReadRecord's "end of the available WAL". The reason is logged once at LOG \(emode\_for\_corrupt\_record\),
  and Next waits at the record's start for new WAL instead of failing. Before this, standby replay stopped for good
  with "standby replay: stopped on error", and the promotion that followed used a replayer that had already exited.
- `runArchiveThenStream` \(cmd/goopg\), for a standby started with `restore_command` and no `primary_conninfo`.
  The archive feeder runs until a reload sets `primary_conninfo`, as WaitForWALToBecomeAvailable switches source.
  Then the feeder stops, `Writer.RewindTo` moves insertion back to the start of the segment holding the next record
  replay needs \(RequestXLogStreaming rounds the start to the segment\), and the walreceiver starts there. The segment
  is re\-received over the archive copy, which past the invalid record is not the WAL the upstream has.
- `Writer.RewindTo` shares `state.resetInsertPosition` with `SwitchTimeline`. The tracker, WAL buffer, sender ring
  and published watermarks restart at the boundary, and open segment files close.
- The promotion's end of log was already correct: standby1 promotes at the start of the torn record \(the end of the
  last valid record\).

- M0149\-0028a: the catalog heap loader \(initdb `loadUserTablesFromHeapForDBMode`\) decodes pg\_class rows with
  `DecodePGClassPhysicalRow` first. A PG primary's first table has oid 16384 \(0x00004000\). The goopg logical
  decoder read that leading 0x00 as its "not null" flag and returned garbage without an error, so the table never
  reached a goopg standby's catalog, at replay or at restart. `deleteCatalogRowsForOID` and the pg\_attribute pass
  already decoded physical\-first.

## Not done

- A standby with both `primary_conninfo` and `restore_command` still uses only the walreceiver. PG alternates the
  archive and streaming sources on failure \(WaitForWALToBecomeAvailable\); goopg switches only from archive to
  streaming, once.
