(idle — nothing in flight)

Loop #125 landed **M0131-S18.1 + S18.2**; S18 stays UNCHECKED because S18.3
(live TLI) + S18.4 (`encodeCheckPointStruct` constants) remain.

Carry-forward:

- **Next pick is the rest of S18 (S18.3 + S18.4, ~1 loop)** — the fix_plan line
  now carries both, and S18.4 gained a fifth item: `encodeCheckPointStruct`
  (`internal/wal/recovery.go:783-794`) never writes payload offsets 12
  (`PrevTimeLineID`) or 16 (`fullPageWrites`) AT ALL, so every goopg checkpoint
  record carries `PrevTimeLineID = 0`. Extend
  `TestPgControlCheckPointCopyMatchesPgControldata` rather than writing a new
  harness. After that: S19 (RISKY, writer half is the load-bearing one),
  then S20/S29.
- **Discovery worth remembering (shaped all the guards):** with pg_control's
  read-modify-write cycle, a missing *encode* line is INVISIBLE to both an
  oracle comparison and a byte-for-byte round-trip — the on-disk value is
  preserved, the field is merely *unsettable*. Only a missing *decode* line is
  destructive (decodes 0, then encode writes that 0 over live data). Any future
  pg_control field work needs a **settability** assertion, not just a
  round-trip.
- **The doc number is `0131-0014`, not `-0013`** (`-0013` is the WAL-reader
  doc). The fix_plan pointer was wrong for the second loop running and is now
  corrected in the S18 line.
- 2 ledger rows filed: the nine new fields are settable but nothing POPULATES
  them from live state (a goopg checkpoint still leaves `nextMulti = 1` /
  `oldestXid = 3` forever — checkpointer work, and multixact durability is the
  deferred S24), and the `PrevTimeLineID`/`fullPageWrites` payload discovery.

Technique reused (fourth loop running): every guard proven fail-when-broken by
scripted revert over a /tmp backup — 5 break directions here (drop encode line,
drop decode line, shift an offset 88→89, restore `os.WriteFile`, and the
no-`O_CREATE` contract), each caught by a *different* assertion.

Gates run this loop: `internal/control` PASS, `internal/initdb` PASS (67 s),
`internal/wal` + `internal/storage` PASS, both cold-start E2Es PASS (27 s),
UNITS PASS, pgbench smoke via the commit hook, `make ralph-state-guard` OK
(auto-repaired the previous loop's clean-exit marker).

In-flight: none.
