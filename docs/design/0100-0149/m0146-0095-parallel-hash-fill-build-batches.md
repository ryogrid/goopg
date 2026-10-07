# M0146-0095 — a build-filling Parallel Hash join batches a spilled share

Status: done 2026-10-08 (2f2eadcee). Parent: M0146-0090.

## Problem

M0146-0090 merged spilled Parallel Hash shares into one batched shared table
(`mergeSpilledParts`). Each participant then probed batch k with its own
probe rows, through the E-09 participant path. That is correct for joins
that emit only from the probe side.

A join that fills its build side (RIGHT, FULL, RIGHT SEMI, RIGHT ANTI) also
emits unmatched build rows. A batch-k build row is unmatched only if no
participant's probe rows matched it, so no single participant could judge
it. Such a join therefore kept refusing a spilled share:

```
... of a join that fills its build side is not supported
```

## PG behaviour

Every participant probes batch k. The last participant to leave the batch's
probe phase scans it for unmatched tuples (`PHJ_BATCH_SCAN`,
`ExecParallelPrepHashTableForUnmatched`, nodeHash.c), with the matched
flags the shared table accumulated.

## Change

goopg already had this protocol for batch 0 (M0146-0005dj): participants
merge private matched bitmaps at probe EOF, and the last to detach sweeps.
Batches past 0 are now routed to that same participant.

1. **Hand-over at detach.** When a participant detaches from batch 0
   (`parallelFillDetach`), it hands its probe-row files for later batches
   to the shared state, together with its matched bits
   (`hashBatchState.handOverOuterFiles`).
   - The writers are closed before hand-over.
   - The mutex is the publication edge.
2. **The sweeper takes every file.** The last participant to detach claims
   the sweep and receives all participants' files as extra outer files of
   its own batch state (`adoptHandedOuterFiles`). Its own probing is
   already finished, so no participant is still writing.
3. **The sweeper runs every later batch.** For each batch k it:
   - loads the shared inner batch through the descriptor's load slots;
   - replays its own probe rows, then the handed-over ones
     (`batchReplayOp` reads a list of files);
   - sweeps with matched bits that cover every probe row of the batch.
4. **Everyone else stops.** Any other participant skips the batch loop.
   Its files are already handed over, and it never sweeps.
5. **The sweep decision is stable.** `parallelFillDetach` called again at a
   later batch's probe EOF returns the batch-0 decision.
6. **Bookkeeping.** `batchSkippable`, `discard` and `close` count the extra
   files.

The NULL-keyed build rows are unchanged: each participant sweeps the ones
its own share recorded.

## Verification

- **`TestParallelHashSpilledBuildMatchesSerial`.** RIGHT and FULL at
  work_mem 16 kB batch (nbatch 4) and return exactly the serial rows, with
  the sweep claimed once.
  - Mutation: skipping the hand-over returns 18120 rows against serial
    58782.
- **Race.** `go test -race` on the TestParallelHash family is clean.
- **No plan change.** The planner's PH4 veto (M0146-0096) still keeps
  Parallel Hash off inners estimated to spill, so this path runs only for
  an underestimated build.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set (no changed
  query), SF0.25 sweep (99/99 shapes unchanged) and ea-ratchet all PASS.

## Deferred (ledgered)

Later batches of a build-filling join run in one participant. PG processes
batches cooperatively: participants attach to batches, and each batch has
its own barrier and last-detacher sweep. This trades parallelism, not
correctness. The resume point is the per-batch analogue of `probeDetach`:
each participant probes the batches it attaches to, merges its batch-k bits
at batch-k detach, and the last detacher sweeps batch k.
