# Milestone 0088 — WAL torn-tail recovery

**Status:** planned
**Depends on:** M0079 (catalog DDL WAL recovery)
**Drives:** crash-safe restart after a non-clean shutdown — goopg
must treat the first invalid record after the last valid one as
end-of-WAL, mirroring PostgreSQL's recovery semantics.

## Context

A pgbench run on 2026-05-11 (`-c 100 -j 100 -T 180`) followed by a
non-clean shutdown (SIGKILL / `pkill -SIGTERM` without enough drain
time) left WAL that fails to replay:

```
goopg start: goopg: wal replay: wal: decode at offset 1308615374:
  wal: corrupt record: checksum mismatch
```

The corrupt record sat ~78 MB before the end of a 1.3 GB WAL — i.e.
more than one segment-size (16 MiB) from EOF. The replay loop at
`internal/wal/reader.go:70-98` (legacy) and `:106-172` (page-aware)
has a graceful end-of-WAL path, but only when
`len(stream) - off ≤ segmentSize`. Beyond that distance any
checksum mismatch is treated as fatal corruption.

PostgreSQL treats the first invalid record after the last valid
checkpoint as end-of-WAL and continues recovery as if the WAL ended
there. goopg needs the same semantic so that any non-clean shutdown
is recoverable, not just one that happens to write its torn record
in the final segment.

A workaround discovered during the pgbench run is to call
`bin/goopg checkpoint` immediately before `bin/goopg stop` — but
that only fixes the *graceful-stop* path; an OOM kill, SIGKILL, or
host crash still produces the same fatal-on-restart symptom.

## Required design docs

- `docs/design/0088-0001-wal-torn-tail-detection.md`
  (look-ahead-zero heuristic; safety argument that "zeros until
  EOF" is a sufficient end-of-WAL signal because the EOS sentinel
  itself is a zero header; trade-off discussion vs the current
  strict-corruption stance; test matrix).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note about the milestone-only convention.

## Definition of Done (sketch)

- `internal/wal/reader.go` decode loops graceful-stop on any
  `ErrCorruptRecord` whose remaining stream bytes are all zero
  (the WAL preallocated-tail signal), regardless of distance from
  EOF.
- Real mid-WAL corruption (CRC mismatch followed by non-zero
  bytes) still errors fatally — distinguishing torn-tail from
  bit-flip corruption.
- Tests:
  - torn-tail-end-of-segment
  - torn-tail-mid-segments (the pgbench failure mode — strict
    pre-fix, graceful post-fix)
  - real-corruption (non-zero tail) propagates
  - integration: SIGKILL'd goopg restarts cleanly after a
    non-trivial write workload.
- No regression in existing complete-WAL recovery tests
  (`TestPreallocatedSegmentRecoversCleanly`,
  `TestCrashRecoveryReplaysWALAfterUncleanShutdown`).
