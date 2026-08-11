# WAL reader fail-closed — an unrecognised record is not end-of-WAL, and a recycled segment is not WAL

**Status:** CLOSED 2026-08-11 — S16 (S16.1/.2/.5 then S16.3/.4) and S19 (S19.1/.2)
**Date:** 2026-08-11
**Milestone:** M0131 (Theme F, S16 + S19)

## Problem

goopg's WAL reader collapses two situations into one outcome. A
**torn/zero/CRC-failed tail** — bytes that were never durable — and a
**decodable-but-unhandled record** — bytes that *are* durable and *do* mean
something — both stop the walk, log a `slog.Warn`, and report success. The second
case is silent data loss, made permanent by the first subsequent write.

Separately, goopg writes and decodes `xlp_pageaddr` but **never compares it**,
and that comparison is upstream's only defence against a recycled WAL segment
full of stale, CRC-valid records. goopg zero-fills recycled segments so it never
needed the check on its own directories; a real PG's `pg_wal` does not.

S16 is a live data-loss fix worth landing even if no other Theme F slice ever
does; S19 also repairs the **clean** reverse path (S3), not only the crash path.

## Design

### S16 — an unrecognised record must not be end-of-WAL

**S16.1 — the structural rmid bound is wrong.** `internal/wal/xlog_record.go:218-220`:

```go
if h.Rmid > MaxKnownRmgr && h.Rmid < RmgrGoopgCustomBase {
    return h, fmt.Errorf("%w: unknown rmid=%d", ErrInvalidRecordHeader, h.Rmid)
}
```

`MaxKnownRmgr = RmgrSeq = 15` (`xlog_record.go:65`, `:71`);
`RmgrGoopgCustomBase = 128` (`:79`). PG 18's real maximum is 21 —
`postgres/src/include/access/rmgrlist.h:28-49` defines 22 resource managers, in
ID order: **0 XLOG, 1 Transaction, 2 Storage (`RM_SMGR_ID`), 3 CLOG, 4 Database,
5 Tablespace, 6 MultiXact, 7 RelMap, 8 Standby, 9 Heap2, 10 Heap, 11 Btree,
12 Hash, 13 Gin, 14 Gist, 15 Sequence, 16 SPGist, 17 BRIN, 18 CommitTs,
19 ReplicationOrigin, 20 Generic, 21 LogicalMessage.**

Raise the bound to PG's real `RM_MAX_BUILTIN_ID` (21) so 16..21 decode as records; keep
rejecting `(21, 128)`, which remains a genuine "emitted by a newer producer"
branch. Note 6, 18 and 19 are *not* index AMs and do occur in ordinary PG
workloads.

**Which rmids take which path today.** The split is the whole point of S16:

- **6 MultiXact, 12 Hash, 13 Gin, 14 Gist** are ≤ `MaxKnownRmgr`, so they pass
  the header guard, decode, fall through `replayDecodedXLogRecord` to
  `default: return false, unsupportedDecodedXLogRecord(r)`
  (`internal/wal/recovery.go:2525-2526`, message built at `:2605-2612`), and
  startup **refuses**. That half is safe.
- **16 SPGist, 17 BRIN, 18 CommitTs, 19 ReplicationOrigin, 20 Generic,
  21 LogicalMessage** fail *header decode* at `:218-220` and are read as
  **end-of-WAL**. Silent loss. A single `pg_logical_emit_message` in a PG crash
  tail is enough.

**S16.2 — split the two meanings collapsed into `endOfWAL`.**
`readAllPageAware` (`internal/wal/reader.go:102-218`) has four failure arms and
every one of them ends in `break`:

| Arm | Condition | Preallocated-tail guard | Silent size guard | Warn |
|---|---|---|---|---|
| header decode | `:152-165` | `:157` | `:160-162` | `:163` |
| `xl_tot_len < 24` | `:167-176` | `:168` | `:171-173` | `:174` |
| body decode | `:183-196` | `:188` | `:191-193` | `:194` |
| size mismatch | `:197-211` | `:202` | `:205-207` | `:208` |

The `if int64(len(stream)-off) <= segSize { break }` guards at `:160`, `:171`,
`:191` and `:205` suppress even the warning for **any tail inside the last
segment** — which is precisely where a crash tail always is. `endOfWAL`
(`reader.go:243-249`) is a `slog.Warn` and nothing more.

Return an explicit stop reason from `readAllPageAware` and surface it through
`ReadAll`: a torn/zero/CRC-failed tail is end-of-WAL, a decodable-but-unhandled
record is an error the caller must see. Delete the four unconditional
`<= segSize` breaks.

Two failure classes ride the *body-decode* arm and are swallowed the same way,
which is why S16.2 is the real fix rather than S16.1:

- **`XLOG_TBLSPC` / unsupported tablespace OID** —
  `internal/wal/pg_xlog_decode.go:362-365`.
- **`wal_compression`** — `pg_xlog_decode.go:345-350` **already** errors
  explicitly (`"wal: compressed PostgreSQL backup block images are not supported
  yet"`), contrary to Theme F's S16.5 framing; see §Citation corrections. The
  error is nevertheless swallowed by `:191-193` and reported as a clean
  end-of-WAL.

**S16.3 — the btree `default:` arm is unsound for PG-authored records.**
`internal/wal/recovery.go:2516-2523`:

```go
default:
    // Other btree records (dedup / delete / reuse-page / …) are
    // not flipped yet and are emitted with full-page images; restore
    // each block from its FPI.
    if err := replayDecodedXLogHeapFPIBlocks(mgr, r, xlog); err != nil {
        return false, err
    }
    return true, nil
```

`replayDecodedXLogHeapFPIBlocks` (`:2535-2545`) **`continue`s** over any block
lacking `HasImage && ImageApply` (`:2537-2539`) and returns nil — so the arm
reports `applied=true` having applied nothing. The comment's premise ("emitted
with full-page images") holds for goopg-emitted records and **not** for PG's:
`XLogRecordAssemble` decides per block, `needs_backup = (page_lsn <= RedoRecPtr)`
(`postgres/src/backend/access/transam/xloginsert.c:620`) — an FPI only on a
page's **first touch after a checkpoint** — and `needs_backup = false`
unconditionally when `!doPageWrites` (`:609-610`, i.e. `full_page_writes=off`).
A PG `XLOG_BTREE_DEDUP` / `_DELETE` / `_INSERT_UPPER` from anywhere but the first
touch after a checkpoint therefore carries main data and no image, and is
**silently discarded while reporting success** — index corruption with no
diagnostic.

Fix: apply FPIs only if **every** mutated block carries `ImageApply`; otherwise
`unsupportedDecodedXLogRecord`. This converts silent corruption into refusal, and
is a hard prerequisite for S21b — without it a btree-opcode regression is
indistinguishable from success.

**S16.4 — audit the `RmgrXLog` `default:`.** `recovery.go:2245-2249` returns
`false, nil` for every unmatched RM_XLOG opcode with the comment *"Other RmgrXLog
opcodes (checkpoint, noop, switch, …) need no physical replay action on the
standby."* True for NOOP / SWITCH / CHECKPOINT_ONLINE / CHECKPOINT_SHUTDOWN /
NEXTOID / RESTORE_POINT / FPW_CHANGE / BACKUP_END / END_OF_RECOVERY /
CHECKPOINT_REDO; not true for `XLOG_FPI_FOR_HINT` (0xA0, silently discards
torn-page protection — S21a) or `XLOG_OVERWRITE_CONTRECORD` (0xD0). Enumerate the
no-op set explicitly and error on anything else.

**S16.5 — `wal_compression`.** Already implemented; keep the slice only to wrap
the error in `ErrCorruptRecord` for consistency with its neighbours and to prove
via test that it now reaches the caller rather than being absorbed by S16.2's
deleted guard.

**The caller chain, end to end.** S16.2's stop reason must reach process exit, so
the whole path matters:

```
readAllPageAware  (reader.go:102)   →  stop reason, today discarded
  ReadAll         (reader.go)
    ReplayFromDirWithMgr (recovery.go:3686-3697)  → returns err unless fs.ErrNotExist
      ReplayRecords      (recovery.go:1918-1932)  → wraps: "wal: replay record %d lsn[%d,%d]: %w"
        ApplyRecord      (recovery.go:2011-2030)  → routes to replayDecodedXLogRecord
      initdb.Open        (open.go:347-350)        → "goopg: wal replay: %w", mgr.Close()
        → non-zero exit
```

Note the asymmetry the fix removes: `ApplyRecord`'s errors already ride this
chain to a refused start (that is why rmids 6/12/13/14 are safe), while
`readAllPageAware`'s do not reach it at all.

### S19 — validate `xlp_pageaddr`; stop trusting recycled segments

**The gap.** goopg has five `PageAddr` sites — field `internal/wal/xlog_page.go:91`,
encode `:120`, decode `:144`, write `internal/wal/xlog_emit.go:125` and
`internal/initdb/wal_bootstrap.go:70` — and **no comparison site anywhere**; a
grep across `internal/` and `cmd/` returns only those plus tests. Upstream
compares at `postgres/src/backend/access/transam/xlogreader.c:1324-1337`, whose
comment names the exact case: *"This check typically fails when an old WAL
segment is recycled, and hasn't yet been overwritten with new data yet."*

goopg got away with it because `recycleSegmentFile`
(`internal/wal/writer.go:2369-2379`) renames **and then zero-fills**, while
upstream's `InstallXLogFileSegment` (`xlog.c:3559`) is a bare `durable_rename`
(`:3598`; the `durable_unlink` at `:3579` is the `!find_free` path only) and
never fills. A real PG's `pg_wal` therefore routinely holds **full-size future
segments packed with stale, CRC-valid records from a previous WAL cycle** — every
row of the end-of-WAL contract except row 10 passes on those bytes.

**S19.1 — the reader half.** In `readAllPageAware`'s page-header block
(`reader.go:129-139`), require `hdr.PageAddr == baseOffset + off` and a
consistent `hdr.TLI`; otherwise stop as end-of-WAL (rows 10/11 of the contract).
Consider validating `xlp_sysid` against `pg_control.system_identifier` while here
— upstream does, at `xlogreader.c:1281-1289`, and M0131-S2 made the identifier
readable.

**S19.2 — the writer half, per Hard-won Rule #2 (sibling paths must agree).**
`scanLastSegmentEnd` (`internal/wal/writer.go:1494`, main walk `:1538-1549`) is a
byte-for-byte sibling of the reader walk and needs the same check. Two adjacent
assumptions in `detectWritePos` (`writer.go:1287`) also break on a PG directory:

- **"non-final segments are fully used"** (`:1433-1439`) — the loop adds the full
  `sz` for every segment but the last and only scans the last one. A PG recycled
  segment sitting between real ones therefore contributes its whole size.
- **the phantom-drop loop** (`:1409-1417`) — it walks back from the highest
  segment while `usedBytes == 0`, i.e. while a segment scans as *entirely empty*.
  A PG recycled segment scans as **non-empty** (stale valid records), so the loop
  breaks on the first iteration and `writePos` lands tens of MB past the true end
  of WAL, **inside garbage**.

**Carry this hedge exactly, do not upgrade it.** The **reader** half is
conditional: it only misbehaves when the last real page ends exactly on a page
boundary, so the walk steps onto the first stale page and reads its header. The
common case is saved by the zero tail of PG's memset WAL buffer, which trips row
1 or row 6 first. The **writer** half is *unconditional* and should reproduce
directly. Design and test accordingly: the writer fixture is the load-bearing
one.

**S19 is not gated on the crash work.** A cleanly shut down PG directory has
recycled segments too, so this repairs S3's clean reverse path as well.

**Risk.** This is the slice in Theme F most likely to break goopg's *own* restart
path — a `PageAddr` mismatch that today is invisible becomes a truncated WAL
tail. Land it behind the pgbench smoke and a crash-restart test, not unit tests
alone.

## Guards

1. **S16.1/S16.2 — the rmid unit test.** Feed `readAllPageAware` a valid record,
   then an rmid-18 record, then more valid records; assert a **non-tail stop**
   (an explicit error surfaced to the caller). Today: 1 record, no error.
2. **S16.3 — the btree unit test.** A PG-shaped `XLOG_BTREE_DEDUP` with block
   data and **no** image now errors instead of returning `applied=true`.
3. **S16.4** — a table-driven test over the enumerated RM_XLOG no-op opcode set,
   plus one unknown opcode asserting refusal.
4. **S16.5** — a compressed-image block reaches the caller as an error rather
   than a clean end-of-WAL.
5. **Caller-chain test** — the S16.1 stream, replayed via
   `ReplayFromDirWithMgr`, produces a non-nil error (proving the reason survives
   `ReadAll` → `ReplayRecords` → `Open`).
6. **S19 — the recycled-segment fixture.** A directory with a real PG segment
   followed by a hand-crafted recycled segment of stale valid records; assert
   `ReadAll` stops at the real end **and** `detectWritePos` returns the real end.
   Both halves, in one fixture.
7. **RACE on `internal/wal`** for S16 and S19.
8. **SMOKE (pgbench) + a crash-restart test** for S19 specifically, plus SPOT
   (`scripts/tpch-spotcheck.sh`) — S19 is flagged as the slice most likely to
   break goopg's own restart.
9. UNITS + SMOKE green.

## Implementation notes — what landed 2026-08-11 (S16.1 / S16.2 / S16.5)

**S16.1.** `internal/wal/xlog_record.go` now names rmids 16..21
(`RmgrSPGist` … `RmgrLogicalMessage`) and `MaxKnownRmgr = RmgrLogicalMessage`
(upstream `RM_MAX_BUILTIN_ID`). The constant is deliberately re-documented as
the *protocol* bound rather than "what goopg can replay": refusing a record
goopg cannot apply is the replay dispatcher's job, where the refusal reaches
the caller instead of being mistaken for a torn tail. `(21, 128)` is still
rejected, so the existing rmid-99 / rmid-127 decoder tests are unchanged.

**S16.2.** New sentinel `ErrUnsupportedRecord` (`format.go`) names the class the
design separates out: bytes that are intact and durable but that goopg cannot
decode or apply, as opposed to `ErrCorruptRecord`'s never-durable ones. In
`readAllPageAware`:

- the four unconditional `int64(len(stream)-off) <= segSize` breaks are gone
  (the `isPreallocatedTail` guards stay — a zero tail is still a silent stop);
- the body-decode arm returns `ErrUnsupportedRecord` to the caller. This is
  sound *because* `decodeRecordXLogDetailed` verifies the record CRC **before**
  parsing the body, so anything raising that sentinel is durable by
  construction;
- the header-decode arm gained `durableUnknownRecord`, which re-checks the
  record's own CRC over `xl_tot_len` bytes. CRC-valid ⇒ a real record from a
  producer this build does not understand ⇒ error to the caller; otherwise ⇒
  end-of-WAL, as before. False-positive cost is a 2^-32 collision producing a
  loud refused start; the false negative it replaces is silent data loss.

**S16.5.** The compressed-image and non-default-tablespace errors are wrapped in
`ErrUnsupportedRecord` so they ride the new path.

**A latent goopg-side data-loss bug this exposed.** The non-default-tablespace
rejection in `decodeXLogBlockRefHeader` did not only affect PG-authored WAL:
goopg's own block-ref encoder writes the real `TblOid` into the locator
(`xlog_assemble.go:130`), so **every** goopg record touching a
`pg_tblspc`-resident relation was rejected by goopg's own decoder and reported
as a clean end-of-WAL — dropping the rest of the stream on goopg's own restart.
It was invisible until the reader stopped swallowing the error, at which point
`TestIndexTablespaceSurvivesRestartViaCatalogHeap` failed with
`unsupported PostgreSQL tablespace OID 16407`. Fixed by carrying the OID into
`RelFileNode.TblOid` (1663/1664 → 0), which is exactly the mapping the sibling
`decodeXLogSmgrCreate` (`recovery.go:4528-4544`) already performed for
`xl_smgr_create`, and which `storage.relDir` (`smgr.go:624-636`) routes through
`pg_tblspc/<oid>/<version dir>/<dbOid>`. Hard-won Rule #2 in miniature: the two
decode siblings disagreed, and only one of them had a test.

**Guards run:** `internal/wal/reader_fail_closed_test.go` (6 new tests: PG-only
rmgrs survive the walk; the dispatcher refuses them; a CRC-valid unknown rmid
errors to the caller; torn tail and zero tail still stop silently; a compressed
image errors; the user-tablespace locator round-trips). Every new guard was
proven fail-when-broken by temporarily restoring the old bound and the
`<= segSize` breaks (4 FAIL, the two tail guards still PASS). `internal/wal`
PASS + `-race` PASS, `internal/initdb` PASS, `internal/storage` PASS, UNITS
PASS, pgbench smoke via the commit hook.

## Implementation notes — what landed 2026-08-11 (S16.3 / S16.4) — S16 CLOSED

The replay-side half. S16.1/S16.2 stopped the *reader* silently truncating the
stream; these two stop the *dispatcher* silently under-applying a record it did
hand to redo. Both former failure modes returned `applied=true`, which is worse
than an error — the caller had no way to tell a real replay from a skipped one.

**S16.3.** New `requireFullPageImages` (`recovery.go`) gates the btree
`default:` arm: every block reference must carry `HasImage && ImageApply`, and
a record with no block references at all is refused too, else the arm returns
`ErrUnsupportedRecord`. The old code called `replayDecodedXLogHeapFPIBlocks`
unconditionally, which `continue`s past an imageless block and then reported
success. This was harmless for goopg's own WAL — every `RecordKind`
`rmgr_map.go` maps to `RmgrBtree` has a named arm above the default — and data
loss for a real-PG crash tail, where PG emits an FPI only on a page's FIRST
post-checkpoint touch, so the second `XLOG_BTREE_DEDUP` on a page carries block
DATA and no image.

**S16.4.** `RmgrXLog`'s opcode space is now enumerated instead of collapsed
into one silent `default: return false, nil`. Three changes:

- The benign set is named explicitly: `CHECKPOINT_SHUTDOWN`,
  `CHECKPOINT_ONLINE`, `NOOP`, `SWITCH`, `BACKUP_END`, `RESTORE_POINT`,
  `FPW_CHANGE`, `END_OF_RECOVERY`, `OVERWRITE_CONTRECORD`, `CHECKPOINT_REDO`.
  Each is a genuine physical no-op for page-level replay; checkpoint contents
  *are* consumed, but via the control-file/redo-start path, not here.
- `XLOG_FPI_FOR_HINT` gets a real arm. Upstream replays it on the same arm as
  `XLOG_FPI` (`xlog.c:8748`); goopg used to drop it into the blanket no-op, so a
  PG hint-bit page was silently DISCARDED. goopg never emits FOR_HINT, so this
  only ever fires on a real-PG crash tail — the path M0131 exists to make work.
- Everything else is refused, **including `XLOG_NEXTOID`**, which is why this
  slice exists: `xlog_redo` sets `nextOid` exactly from it
  (`xlog.c:8292-8308`), so dropping one lets goopg re-issue OIDs a crashed PG
  had already allocated after the last checkpoint. Real redo is S21a; until
  then a refused start beats a rewound OID counter. Ledgered.

**One live self-inflicted hazard found and avoided — the reason S16.4 was
flagged.** `xlogInfoDefault` (0xF0) is *not* a PG opcode: it is goopg's own
`classifyXLogRecord` marker for an EMPTY-payload record
(`format.go:151-153`), and it lands on `RmgrXLog`. The first cut of the refusing
`default:` arm therefore made goopg refuse *its own* WAL — caught by the
existing `TestApplyRecordPrefersDecodedXLogForUnknownPayloadKind`, not by the
new guards. 0xF0 is now in the benign set with the reasoning inline. This costs
no real-PG coverage: PG's opcode space is the high nibble only and it defines
nothing at 0xF0, so a real PG producer can never emit the value. It also
removes 0xF0 from the "undefined opcode" refusal cases — 0xC0 is now the only
free slot goopg does not claim.

**Guards** (`internal/wal/reader_fail_closed_test.go`, all proven
fail-when-broken by scripted patches over a /tmp backup — 3 tests FAIL with the
arms reverted, and the 10 benign no-op subtests correctly kept PASSing):
`TestReplayRefusesBtreeFallbackWithoutFullPageImages` (4 subtests: no blocks,
single imageless block, imageless block in either position, plus the
counterweight that an all-image record is still accepted),
`TestReplayRmgrXLogOpcodeCoverage` (11 benign + 2 refused), and
`TestReplayAppliesXLogFPIForHint`.

**Crash-restart gate** (what the plan asked for instead of unit tests alone):
`goopg init` → btree-heavy workload on a PK + a secondary index (20k inserts, a
modulo DELETE, 10k more inserts, VACUUM) → `kill -9` → restart → repeat with a
second uncheckpointed round (15k inserts + a modulo DELETE). Row counts are
identical across both crashes (23334, then 36429), index lookups still resolve,
and the restart logs contain **zero** refusal/unsupported lines — so neither
S16.3 nor S16.4 turned a today-silent no-op into a refused start on goopg's own
WAL. (Note: the run must be on a genuinely free port — the first attempt bound
nothing because an orphaned server from an earlier session held 5533, and the
workload silently landed in *its* cluster.)

**S16 is now CLOSED.** S16.1/.2/.5 landed in the prior loop, S16.3/.4 here.

## Implementation notes — what landed 2026-08-11 (S19.1 / S19.2) — S19 CLOSED

**The shared port.** `xlogPageValidator` (`internal/wal/xlog_page.go`) is
goopg's port of `XLogReaderValidatePageHeader` *plus* the two cross-page fields
upstream keeps in `XLogReaderState` (`latestPagePtr` / `latestPageTLI`). It
carries five rules: long-vs-short header form must match the page's position in
the segment; `xlp_seg_size` and `xlp_xlog_blcksz` must match this cluster's
geometry; `xlp_pageaddr` must equal the address the page is stored at; and
`xlp_tli` must not go backwards relative to the last page seen *at a lower
address* (upstream's re-read tolerance, kept verbatim). One type, two call
sites — the reader and writer halves cannot drift, which is what Hard-won Rule
#2 is asking for here.

**`xlp_sysid` is deliberately NOT checked** — ledgered. The validator has no
`pg_control` handle and threading one in reaches the crash-recovery path;
the two geometry cross-checks are the cheap two-thirds of upstream's triple.

**The design's hedge was right about the writer half and wrong about *why* the
reader half is conditional.** The writer fixture reproduced immediately:
`detectWritePos` returned **32904** against a true end of **136**, i.e. a whole
segment past the end, inside stale bytes — exactly the predicted failure. The
reader fixture, however, did *not* reproduce at first, and the reason was a
sixth hole the design did not name:

> **`extractRecordBytes` swallows page headers without looking at them.** A
> record that straddles a page boundary is reassembled by skipping the
> intervening header bytes. So validating only where the *walk* lands on a
> boundary means a stale page is caught or missed **depending on record
> tiling** — with 400 uniform records the boundary always fell inside a record
> and the stale page was never examined at all. This is not upstream's
> behaviour: `XLogReadRecord` validates each page as it reads it, continuation
> pages included.

Hence `xlogPageValidator.checkSpan`, called by both walkers after
`extractRecordBytes` and before the assembled bytes are trusted: it validates
every page header strictly inside the record's byte span. With it, the reader
fixture stops at the real end; without it, all 400 records come back and the
stale page is replayed as live WAL. The design's "conditional on the last page
ending exactly on a boundary" statement was therefore *describing the bug in the
fix*, not a property of the WAL: with `checkSpan` the reader half is
unconditional too.

**Where the checks sit.** Reader (`readAllPageAware`, `reader.go`): the
contrecord pre-skip (validate before trusting `xlp_rem_len` — a stale header can
carry a previous cycle's `rem_len` and skip an arbitrary distance), the
page-boundary branch, and the post-`extractRecordBytes` span. Writer
(`scanLastSegmentEnd`, `writer.go`): the same three. The writer's first-page
failure returns `usedBytes = 0`, which is what lets `detectWritePos`'s
phantom-drop loop step over the segment — no change was needed to the loop
itself or to the "non-final segments are fully used" rule, because a recycled
segment now scans as empty and is dropped before either can misfire.

**An all-zero header is still checked first, everywhere.** That is goopg's own
preallocated-tail sentinel and must keep meaning end-of-WAL, not "corrupt page";
routing it through the validator would only add a spurious warning.

**Guards** (`internal/wal/recycled_segment_test.go`), all proven
fail-when-broken by scripted revert over a `/tmp` backup, four break directions,
each caught by a *different* assertion:

1. `TestDetectWritePos_IgnoresPGRecycledFutureSegment` — a real segment 0
   followed by a byte-for-byte copy of it installed as segment 1, which is
   precisely what `durable_rename` recycling produces. Load-bearing half.
2. `TestReadAll_StopsAtStalePageAddr` — page 1's `xlp_pageaddr` falsified in
   place, everything else about the page left valid; asserts a non-vacuous
   baseline first, then that no surviving record reaches past the stale page.
3. `TestXLogPageValidatorMatchesUpstreamChecks` — one subtest per ported rule,
   so a future loosening is named rather than inferred.

Note the older `TestDetectWritePos_*` fixtures use `segSize = 1024`, which is
**smaller than `XLOGBlockSize`** and therefore never crosses a page boundary at
all; the S19 fixtures use `4 * XLOGBlockSize` so page headers actually exist
where the walkers expect them.

**Gates:** the 3 new guards PASS + each proven failing without the fix;
`internal/wal` PASS (5.7 s) and RACE PASS (8.2 s); `internal/initdb` PASS (65 s,
covers the crash/recovery fixtures); `internal/testport` `TestPort_Recovery` +
`TestE2E_` PASS (112 s); **crash-restart smoke** — fresh cluster, `pgbench -i -s
5`, 18 353 txns, `SIGKILL` the postmaster via `postmaster.pid`, restart:
`count|sum` identical across the crash (`500000|-301853`) and a further pgbench
run clean at 924 tps; SPOT `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12 rows=2,
Q13 rows=35). *Trap re-encountered:* the first crash-restart attempt silently
talked to an **orphaned server from an earlier session** already bound to 5533
(`bind: address already in use` was buried in the log while `pg_isready`
answered happily) — always grep the server log for that string before believing
a lifecycle result.

## References

**Citation corrections vs Theme F** (re-checked against the tree 2026-08-11; every
other S16/S19 citation holds): the `<= segSize` silent breaks are `:160-162`,
`:171-173`, `:191-193`, `:205-207` — Theme F's `:157`/`:169`/`:186`/`:198` are the
neighbouring `isPreallocatedTail` guards; `endOfWAL` is `:243-249` (`:212-241` is
its doc comment) and `readAllPageAware` spans `:102-218` with its header-decode
arm at `:152-165`; `detectWritePos`'s "fully used" assumption is `:1433-1439`;
`PageAddr` has five sites, not three; **S16.5 is substantially discharged**
(`pg_xlog_decode.go:345-350` already errors on the compress bit, in the
block-header decoder rather than `decodeXLogBlockImage:387-408`); upstream ranges
are `xlogreader.c:1324-1337` (pageaddr), `:1217-1223` (CRC), `:769-778`
(contrecord length).


- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
  §"Theme F" — S16, S19
- `docs/design/0131-0012-crash-state-cluster-dir-interchange.md` §"The end-of-WAL
  contract" (rows 1–16 are the contract S16.2 and S19.1 implement)
- `postgres/src/include/access/rmgrlist.h:28-49`
- `postgres/src/backend/access/transam/xlogreader.c:1142-1190`, `:1234-1370`,
  `:626-632`, `:757-778`
- `postgres/src/backend/access/transam/xloginsert.c:604-647` — FPI decision
- `postgres/src/backend/access/transam/xlog.c:3558-3608` —
  `InstallXLogFileSegment`
- memory: `pattern_sibling_paths_must_agree` (Hard-won Rule #2),
  `wal_pg_faithful_rmgr_dispatch_preference`,
  `goopg_wal_xl_prev_1based_pg_waldump`
