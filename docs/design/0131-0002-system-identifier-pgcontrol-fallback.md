# `LoadOrCreateSystemID` reads pg_control first — stop inventing a system identifier on a PG-authored directory

**Status:** accepted (landed 2026-08-11, M0131-S2)
**Date:** 2026-08-11
**Milestone:** M0131 (S2)

## Problem

`LoadOrCreateSystemID` (`internal/initdb/initdb.go:54-76`) knows exactly one
source of truth: the goopg-private flat file
`global/system_identifier` (`initdb.go:47`). When that file is absent it calls
`rand.Read`, writes the result, and returns it (`initdb.go:66-75`).

On a directory that PG's initdb created, that file does not exist — but
`global/pg_control` does, and it already carries a `system_identifier` assigned
once at `BootStrapXLOG` from wall clock + pid
(`postgres/src/backend/access/transam/xlog.c:5099-5101`, stored via
`InitControlFile`, `:4217`). goopg therefore **invents a second, different
cluster identity** and persists it beside PG's.

That value is not cosmetic. `Open` passes it into the WAL writer as
`SystemID` (`internal/initdb/open.go:354-359`, `:409`;
`internal/wal/writer.go:131-135`, `:1269`), where it is stamped as `xlp_sysid`
into every long WAL page header — the field a reader compares against its own
`system_identifier` and rejects on mismatch
(`postgres/src/backend/access/transam/xlogreader.c:1282-1286`). The same value is
reported by `IDENTIFY_SYSTEM` (`cmd/goopg/main.go:594-595` →
`internal/server/replication.go:139`) and by BASE_BACKUP
(`internal/server/basebackup.go:338`). So on a PG-authored directory goopg
would emit WAL that PG's own reader repudiates, and advertise a system identity
that disagrees with the pg_control in the same directory.

This is silent state corruption, not a cosmetic divergence: nothing fails at
start-up, the flat file is written, and the disagreement only surfaces later, at
a replication attach or a `pg_waldump` cross-check. It is also *exactly* the bug
M0130-S8.1 already fixed for the timeline ID, without generalising the lesson.

## Design

### Make S2 the sibling of the timeline fix

`LoadOrCreateTimelineID` (`internal/initdb/timeline.go:44-68`) is the shape to
copy verbatim. Its resolution order, stated in its own doc comment
(`timeline.go:1-16`, `:37-43`):

1. pg_control `checkPointCopy.ThisTimeLineID` — authoritative.
2. `global/timeline_id` flat file — fallback when pg_control is absent or
   unreadable.
3. `BootstrapTimeLineID` (1) — fresh cluster; write the flat file.

and when both exist and disagree, pg_control wins and the flat file is
**corrected** (`timeline.go:49-56`), not the other way round.

The read is `control.ReadControlFile(dataDir)`
(`internal/control/pgcontrol.go:237-262`), which returns `(nil, nil)` for an
absent file and **verifies CRC32C over the first 292 bytes**
(`pgcontrol.go:251-255`, offset `pgControlCRCOffset = 292`), returning an error
on mismatch precisely so callers can fall back to the secondary copy. A
placeholder or corrupt pg_control therefore cannot poison the result — that CRC
gate was added for M0130-S8 and S2 inherits it for free.

goopg change:
- `internal/control/pgcontrol.go`: `ControlFileData` currently exposes no
  `system_identifier` field — the struct (`pgcontrol.go:44`) starts at `State`
  (offset 16, `:45-46`). Add `SystemIdentifier uint64` and decode it from
  `buf[0:8]` in `decodeControlFileData` (`:116`). Upstream's layout puts
  `system_identifier` first in `ControlFileData`
  (`postgres/src/include/catalog/pg_control.h:104-110`; the version fields are
  deliberately 8 bytes in, `:112-117`), so offset 0 is correct.
  Do **not** add it to `encodeControlFileData` (`:150`): `UpdateControlFile`
  (`pgcontrol.go:205-235`) reads the whole 8192-byte buffer, mutates only the
  fields the encoder writes, and preserves every other byte — so a decode-only
  field is round-trip safe and cannot be clobbered by a checkpoint. If it is
  added to the encoder as well, every `UpdateControlFile` caller must be audited
  to confirm it never leaves the field zero.
- `internal/initdb/initdb.go:54-76`: restructure `LoadOrCreateSystemID` to the
  three-way order — pg_control, then flat file, then random generation — and
  write the flat file from the pg_control value so every subsequent start is
  unchanged and the fast path stays a single 8-byte read.

### Precedence when both exist

Three cases, and the third is the one that needs a decision:

| flat file | pg_control | behaviour |
|---|---|---|
| absent | present, CRC ok | adopt pg_control's ID; write the flat file from it (the PG-authored-directory case) |
| absent | absent / CRC bad | generate randomly and write the flat file — a genuinely fresh `goopg init` |
| present | present, disagreeing | **pg_control wins**; overwrite the flat file; log at WARNING |

pg_control-wins is the PG-faithful answer: upstream has no second copy at all,
and `system_identifier` is assigned exactly once, at bootstrap. A goopg-private
file that disagrees with pg_control is by definition the stale one — the same
argument `timeline.go:49-56` already makes for the TLI. The WARNING matters
because the only ways to reach that state are a crash between the two writes and
a hand-edited directory, and both are worth a log line.

The fresh-`goopg init` path stays random by construction, with no ordering
change required: `Init` calls `LoadOrCreateSystemID` at `initdb.go:1250`, and the
only pg_control writer in the init sequence is `writePgControl` at `:1264`,
fourteen lines later (with `WriteBootstrapWAL` at `:1258` consuming the ID in
between). At `:1250` there is no pg_control to read, so the pg_control-first
probe falls through to generation exactly as today. Verify this ordering still
holds when the change lands rather than assuming it: an earlier pg_control write
introduced anywhere above `:1250` would silently change `goopg init` semantics.

### What this does not do

Reconciling a *disagreeing* system identifier with WAL already written under the
old value is out of scope — there is no rewrite path, and the reverse cold start
is defined only for a cleanly shut down source
(`docs/design/0130-0002-pg-class-heap-persistence.md` §"WAL replay constraint").
S2 makes goopg adopt the directory's identity before it writes anything; it does
not repair a directory goopg has already written a divergent identity into.

## Guards

1. New unit test: stage a directory containing a valid `global/pg_control` (CRC
   correct) with a known `system_identifier` and **no** `global/system_identifier`;
   `LoadOrCreateSystemID` returns that identifier and creates the flat file with
   the same 8 bytes. Fails before the change (returns a random value).
2. Disagreement test: flat file and pg_control both present with different
   values ⇒ pg_control's value is returned and the flat file is rewritten to
   match.
3. Fresh-init test: neither file present ⇒ a random non-zero ID is generated and
   persisted; two successive calls return the same value.
4. Corrupt-pg_control test: a pg_control whose CRC does not validate is ignored,
   and the flat file (or generation) is used — asserting the
   `control.ReadControlFile` error path, not a panic.
5. `Init()` still produces a directory whose `global/system_identifier` equals
   the `system_identifier` in the pg_control it writes, and whose bootstrap WAL
   segment carries the same value as `xlp_sysid`.
6. UNITS + SMOKE green.

## References

- `internal/initdb/initdb.go:47` — `systemIdentifierFile`; `:54-76` — `LoadOrCreateSystemID`; `:1250`/`:1258`/`:1264` — init ordering
- `internal/initdb/timeline.go:37-68` — `LoadOrCreateTimelineID` (M0130-S8.1), the shape to mirror
- `internal/control/pgcontrol.go:44-46` — `ControlFileData` (no `system_identifier` field today); `:116` `decodeControlFileData`; `:150` `encodeControlFileData`; `:205` `UpdateControlFile`; `:237-262` `ReadControlFile` + CRC32C gate (`pgControlCRCOffset = 292` at `:26`)
- `internal/initdb/open.go:354-359` — `Open` caller; `internal/wal/writer.go:131-135` — `SystemID` → `xlp_sysid`
- `internal/server/basebackup.go:338`, `cmd/goopg/main.go:594-595`, `internal/server/replication.go:139` — other consumers
- `postgres/src/include/catalog/pg_control.h:104-117` — `system_identifier` at offset 0
- `postgres/src/backend/access/transam/xlog.c:5099-5101` — bootstrap generation; `:4217` `InitControlFile`; `:2137` `xlp_sysid` from `ControlFile`
- `postgres/src/backend/access/transam/xlogreader.c:1282-1286` — reader rejects a mismatched `xlp_sysid`
- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S2
- `docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md` — the timeline precedent

## Outcome (landed 2026-08-11)

Implemented as designed, with no deviation from the three-way resolution order.

- `internal/control/pgcontrol.go`: `ControlFileData.SystemIdentifier uint64`
  decoded from `buf[0:8]`. **Decode-only** — `encodeControlFileData` deliberately
  does not write it back, so `UpdateControlFile`'s read-mutate-write cycle
  preserves the original bytes and a caller that leaves the field zero cannot
  clobber the cluster identity.
- `internal/initdb/initdb.go`: `LoadOrCreateSystemID` restructured to
  pg_control → flat file → random, with the flat file (re)written from the
  resolved value and a WARNING log on the disagreement case. Split out
  `readSystemIDFile` / `writeSystemIDFile` so the flat-file access mirrors
  `readTimelineIDFile` / `WriteTimelineID`.
- The `Init` ordering assumption was re-verified rather than assumed:
  `LoadOrCreateSystemID` at `initdb.go:1250` still precedes the only
  init-sequence pg_control writer, `writePgControl` at `:1264`, so a fresh
  `goopg init` still falls through to random generation unchanged.

Guards 1-5 landed as `internal/initdb/systemid_pgcontrol_test.go`
(`TestLoadOrCreateSystemID_AdoptsPgControl`, `…_PgControlWinsOnDisagreement`,
`…_FreshCluster`, `…_CorruptPgControlFallsBack`,
`TestInitSystemIDMatchesPgControlAndBootstrapWAL` — the last asserting the flat
file, pg_control offset 0, and the bootstrap segment's `xlp_sysid` at bytes
24:32 all agree).
