(idle — nothing in flight)

Loop #124 CLOSED **M0131-S16** by landing its replay-side half (S16.3 + S16.4).
S16.1/.2/.5 landed in loop #123; all five subslices are now done.

Carry-forward:

- **The next Theme F pick is S19** (validate `xlp_pageaddr`/`xlp_tli`; a
  recycled PG segment is full of stale CRC-valid records). Same design doc
  `0131-0013`. Flagged RISKY — the *writer* half
  (`detectWritePos`/`scanLastSegmentEnd`) is the load-bearing one, not the
  reader half. S18/S20/S29 are the other cheap-ish M0131 picks.
- **Discovery worth remembering:** `xlogInfoDefault` (0xF0) is NOT a PG opcode
  — it is goopg's own `classifyXLogRecord` marker for an EMPTY-payload record
  (`format.go:151-153`) and it rides `RmgrXLog`. Any future "enumerate the
  RmgrXLog opcodes and refuse the rest" work must keep 0xF0 benign or goopg
  refuses its own WAL. The first cut of S16.4 did exactly that and was caught
  by the PRE-EXISTING `TestApplyRecordPrefersDecodedXLogForUnknownPayloadKind`
  — the new guards were all green. PG's opcode space is the high nibble only
  and defines nothing at 0xF0, so 0xC0 is the sole free slot goopg does not
  claim.
- **Environment hazard hit this loop:** an orphaned `goopg-sub` server (pid
  1510790, from an earlier session) holds port **5533**. The first crash-gate
  run bound nothing and its workload silently landed in *that* cluster (table
  dropped afterwards). Use 5539+ for throwaway servers until 5533 is reaped.
- 3 ledger rows filed: NEXTOID redo (S21a), native btree redo for the unflipped
  opcodes (S21b — those PG tails are now refused rather than silently lossy),
  and upstream's FPI-vs-FPI_FOR_HINT image-tolerance asymmetry.

Technique worth reusing (third loop running): every new guard proven
fail-when-broken by scripted reverts over a /tmp backup — 3 FAIL, and the 10
benign no-op subtests correctly kept PASSing. Plus, for a replay-side change,
the crash-restart gate (kill -9 over a btree-heavy workload, twice) is what
actually proves goopg still replays its OWN WAL; unit tests cannot.

Gates run this loop: `internal/wal` PASS + `-race` PASS, `internal/initdb` PASS
(65 s), `internal/control` + `internal/storage` PASS, crash-restart gate PASS
(23334 and 36429 rows preserved across two SIGKILLs), UNITS PASS, pgbench smoke
via the commit hook, `make ralph-state-guard` OK.

In-flight: none.
