(idle — nothing in flight)

Loop #126 landed **M0131-S18.3 + S18.4**; **S18 is now checked off — complete**.

Carry-forward:

- **Next pick per the banner is M0131-S19** (RISKY, est ~2 loops — validate
  `xlp_pageaddr`/`xlp_tli`, stop trusting recycled segments). Its writer half
  (`scanLastSegmentEnd` / `detectWritePos` in `internal/wal/writer.go`) is the
  load-bearing one and reproduces unconditionally; the reader half is
  conditional on the last page ending exactly on a boundary. Land with a
  crash-restart test, not unit tests alone — it is the most likely slice to
  break goopg's own restart. After that: S20, then S29.
- **New reusable seam:** `wal.CheckPointFields` + `EncodeCheckpointPGFields`.
  Anything that needs to publish live cluster state into a checkpoint now adds
  a member there and a hook on `CheckpointerConfig`; `runCheckpoint` samples
  ONCE and both the WAL record and pg_control read that struct, so the two can
  no longer drift. `withDefaults()` holds the PG-faithful floors.
- **Discovery:** two long-standing values were wrong, not just unset —
  `oldestCommitTsXid`/`newestCommitTsXid` were `3` where PG writes `0` with
  `track_commit_timestamp` off, and `oldestXidDB`/`oldestMultiDB` were `0`
  where PG's bootstrap writes `Template1DbOid` (1). The reference cluster's
  `pg_controldata` is the cheapest oracle for this class of question —
  `pg_controldata -D bench/tpch/runtime/pgdata`.
- 2 ledger rows filed: `oldestXid`/`oldestMulti` still publish the bootstrap
  floor (the datfrozenxid horizon lives inside the CLOG-truncation closure and
  is computed AFTER the marker is durable), and the multixact counter is
  in-memory-only and never seeded back on restart (S20.4 owns the reader half,
  S24 the SLRU).

Technique reused (fifth loop running): every guard proven fail-when-broken by
scripted revert over a /tmp backup — 8 break directions here (drop the
PrevTimeLineID encode, drop the fullPageWrites byte, restore commitTs=3 +
oldestXidDB=0, re-hardcode the checkpointer TLI, re-hardcode full_page_writes,
and the same three against the pg_control writer), each caught by a *different*
assertion, with the real `pg_controldata` as the independent oracle.

Gates run this loop: 4 new guards PASS + each proven failing without the fix,
`internal/wal` PASS (7 s), `internal/control` PASS, `internal/initdb` PASS
(68 s), cold-start + standby E2Es PASS (34 s), UNITS PASS, pgbench smoke via
the commit hook, `make ralph-state-guard` OK (auto-repaired the previous loop's
clean-exit marker).

In-flight: none.
