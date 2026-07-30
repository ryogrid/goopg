# root-0032 — a crash must not leave the cluster unstartable: WAL stream anchoring and end-of-WAL

Status: accepted
Date: 2026-07-28
Area: `internal/wal` (reader/replay + `detectWritePos`), startup path `internal/initdb.Open`
Milestone: M-NIGHTLY (triage of AI-20260725-011243-008..-026's surviving divergences)

## 1. What was actually broken

M-NIGHTLY's open item was "genuine sub-timeout regress divergences" for
`index_including`, `portals_p2`, `select`, `select_distinct`. Re-running the
alphabetical prefix through `select_distinct` (176 cases, 670 s — the cheap
method from root-0031) showed that **three of those four cases never ran**:

```
=== RUN   TestPort_RegressSuite/misc
    deferred: execution timeout: psql killed after 1m59.999s
=== RUN   TestPort_RegressSuite/misc_functions
    previous case timed out; restarting the cluster to drop orphaned backends
    deferred: cluster restart failed: start failed; process exited early
… 53 further cases, all "cluster restart failed"
```

root-0029 made the harness restart the cluster after any per-case timeout. The
restart itself then failed, and **every case from `misc_functions` (#123) to
`select_distinct` (#176) reported a phantom `deferred:`** — the same shape of
cascade root-0029 fixed one layer up. The preserved server log gave the cause:

```
goopg start: goopg: wal replay: wal: decode at offset 771751920:
             wal: invalid record header: unknown rmid=31
```

So the real defect is not a regress divergence at all: **goopg could not start
after a crash.** Once the WAL got large enough for retention to run, a SIGKILL
left the data directory in a state the startup path treated as fatal.

## 2. Reproduction

`goopg init` → start under the cgroup cap → `pgbench -i -s 5` → `pgbench -c 16
-j 4 -T 200` → `kill -9` at t=170 s → restart. At ~200 MB of WAL the restart
succeeded; at ~570–610 MB (35+ segments, i.e. after retention had run at least
once) it failed, and it failed in two different ways on two different runs:

| run | pg_wal | failure |
|---|---|---|
| 1 | `{1,2}` + `0x0C..0x30` | `goopg: wal: wal: gap at segment 000000010000000000000003` |
| 2 | contiguous | `wal replay: decode at offset 150994936: invalid record header: padding bytes nonzero (0xff 0x09)` |

Both are permanent: every subsequent `goopg start` on that directory fails the
same way. There is no recovery path short of discarding the data directory.

## 3. Three causes, one theme

The theme is that both scanners — the replay walk and the writer's
resume-position scan — assumed the on-disk stream *begins on a record boundary
and stays valid to the end*. Neither holds after a crash.

### 3.1 A hole in pg_wal was fatal (`gap at segment N`)

`removeOldSegments` sorts the obsolete segments **newest-first** (it recycles the
newest into future slots before unlinking the rest, writer.go:2139). A SIGKILL
mid-pass therefore leaves the *oldest* obsolete segments on disk with a hole
above them — run 1's `{1,2}` next to a live run at `0x0C..0x30`.

`firstAvailableSegment` returned the globally smallest segment, and
`readStreamFrom` stops at the first missing file, so replay saw only the two
orphans; `detectWritePos` raised `wal: gap at segment N` and the server refused
to start.

Fix: the live stream is the **longest contiguous run ending at the highest
segment** (`liveSegmentRunStart`), used by both `firstAvailableSegment` (replay)
and `detectWritePos` (writer), so the two agree on where the stream begins.
Skipping the orphaned prefix is safe by construction: only retention creates
holes, and retention only removes segments below the checkpoint keep point, so
the existence of a hole proves everything below it was already checkpointed.
The next checkpoint's retention pass unlinks the leftovers (it re-lists the
directory and removes everything `< keepSeg`).

Upstream never faces this choice because `StartupXLOG` begins at the
checkpoint's REDO location rather than at the first retained segment.

### 3.2 A segment opening with a continuation was read as a record (data loss)

When a record straddles a segment boundary the next segment's first page carries
`XLP_FIRST_IS_CONTRECORD` + `xlp_rem_len`, and its first `MAXALIGN(rem_len)`
bytes are that record's tail. Both scanners decoded those bytes as a record
header.

For `scanLastSegmentEnd` (the writer's resume scan) the decode failed and the
segment was reported as holding only its 40-byte page header, so **a reopened
writer resumed at the top of the segment and appended over already-durable
records**. This is not crash-only: the negative control on the new test shows
54–97 records destroyed per reopen on a *clean* close/reopen cycle, for every
payload size whose records straddle the boundary. It is also what manufactures
the stale bytes beyond the new write position that a later crash restart hits as
`unknown rmid=N`.

For `readAllPageAware` the same bytes appear whenever the live run's first
segment continues a record from a segment retention has removed.

Fix: both scanners skip `hdr + MAXALIGN(xlp_rem_len)` when the first page of the
stream/segment is flagged as a continuation — upstream's rule when it picks a
page up mid-record (`xlogreader.c`).

### 3.3 An unreadable tail was fatal rather than end-of-WAL

Run 2's boundary, dumped from the preserved directory:

```
segment 0x09, last 8 bytes : 5a00 0800 0000 0000      ← start of a record header
segment 0x0A, page header  : info=0x0002 (XLP_LONG_HEADER), xlp_rem_len=0
                                                      ← no contrecord flag
```

WAL bytes are written by concurrent appenders, so a SIGKILL can leave the first
8 bytes of a record header durable at the end of one segment while the page
header that would have flagged its continuation was never written. Upstream
handles exactly this: `ReadRecord`/`report_invalid_record` log the reason and
**end redo at the last valid record** (`xlogrecovery.c`). goopg returned the
decode error from `ReadAll`, `initdb.Open` wrapped it as `goopg: wal replay: …`,
and the server refused to start.

Fix: `readAllPageAware` stops at the first record that fails validation and logs
where and why (`endOfWAL`), instead of returning an error. Everything before the
stop point is replayed; a record whose bytes are not intact was never durable,
so nothing replayable is dropped. The pre-existing silent stop conditions
(all-zero tail, within one segment of EOF) are unchanged — this only converts
the remaining fall-through from fatal to logged.

## 4. What changed

| file | change |
|---|---|
| `internal/wal/reader.go` | `firstAvailableSegment` → live-run anchor; new `liveSegmentRunStart`; leading-contrecord skip in `readAllPageAware`; new `endOfWAL` + four fatal returns converted to a logged stop |
| `internal/wal/writer.go` | `detectWritePos` trims to the live run instead of raising `gap at segment N`; `scanLastSegmentEnd` skips a leading contrecord |

Tests (`internal/wal/reader_segment_gap_test.go`):

- `TestSegmentGapFromInterruptedRetention` — models the interrupted retention
  pass; asserts replay returns the live run (not the orphans) and that the
  writer opens and appends past the last recovered record.
- `TestReopenAfterSegmentStraddlingRecord` — sweeps payload sizes, asserts no
  durable record is lost across a reopen, and **fails if no size produced a
  contrecord segment start**, so it cannot silently stop covering the case.
- `TestLiveSegmentRunStart` — the run-selection rule, including the no-hole case.
- `TestDetectWritePos_GapDetectionAfterNonZeroStart` was inverted: it asserted
  the gap error; it now asserts the live-run resume position. The old contract
  was the bug.

## 5. What is still broken (deferred, ledger row filed)

With the read side fixed, the same reproduction now gets *past* the read and
fails in redo:

```
wal replay: replay record 724960 lsn[140631793,140631872]:
            xlog heap-update add new tuple: storage: not enough free space in page
```

`ReplayRecords` does start at the last checkpoint record (`replayStart`), so this
is a post-checkpoint record that will not apply — an apply-side idempotency gap,
not a stream-anchoring one, and a separate investigation. The reproduction
script is preserved with the ledger row. Until that is fixed, a crash under
sustained write load can still leave a cluster that will not start — the failure
has simply moved one stage later, from "cannot read the WAL" to "cannot apply
it".

Also deferred: the regress harness still emits a phantom `deferred:` per case
after a failed restart (53 of them in the run that opened this investigation),
which is indistinguishable in the nightly log from a genuine divergence.

## 6. Gates

`go test ./internal/wal/ ./internal/initdb/ ./internal/storage/` PASS;
negative control on both halves of the fix (each new test fails with its fix
disabled); end-to-end crash reproduction advances from "cannot start" to the
§5 redo error; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
