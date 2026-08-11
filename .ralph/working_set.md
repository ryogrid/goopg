(idle — nothing in flight)

Loop #123 landed the data-loss half of **M0131-S16** (S16.1 + S16.2 + S16.5):
an unrecognised WAL record is no longer end-of-WAL.

Carry-forward:

- **S16 stays UNCHECKED**: S16.3 (btree `default:` arm must refuse unless every
  mutated block carries `ImageApply`) and S16.4 (enumerate the `RmgrXLog` no-op
  opcodes; `XLOG_NEXTOID` moves to S21a) are still open. Both are replay-side
  and can turn a today-silent no-op into a refused start on goopg's OWN WAL, so
  they need a crash-restart gate (SIGKILL + restart over a btree-heavy
  workload), not unit tests alone. Ledgered; design `0131-0013` §Implementation
  notes lists them.
- **The next Theme F pick is S19** (validate `xlp_pageaddr`/`xlp_tli`; a
  recycled PG segment is full of stale CRC-valid records). Same design doc.
  Flagged RISKY — it is the slice most likely to break goopg's own restart, and
  the *writer* half (`detectWritePos`/`scanLastSegmentEnd`) is the load-bearing
  one, not the reader half.
- **Discovery worth remembering:** goopg's own emitter writes the real `TblOid`
  into a block-ref locator, but the decoder rejected every non-1663/1664
  tablespace OID — so every record touching a `pg_tblspc`-resident relation was
  read as a clean end-of-WAL **on goopg's own restart**. Invisible until the
  reader stopped swallowing decode errors. Fixed here; the sibling
  `decodeXLogSmgrCreate` had had the right mapping all along (Hard-won Rule #2).
- Nightly `20260811-014635` (12 items) was already filed at fix_plan.md:717 —
  re-verified this loop, do not re-file.

Technique worth reusing (same as last loop): every new guard was proven
fail-when-broken by temporarily restoring `MaxKnownRmgr = RmgrSeq` and the four
`<= segSize` breaks via a scripted patch over /tmp backups — 4 FAIL, and the two
"still end-of-WAL" guards correctly kept PASSing.

Gates run this loop: `internal/wal` PASS + `-race` PASS, `internal/initdb` PASS
(70 s — it caught the tablespace bug), `internal/storage` PASS, UNITS PASS,
pgbench smoke via the commit hook, `make ralph-state-guard` OK.

In-flight: none.
