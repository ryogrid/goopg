(idle — nothing in flight)

M0131-S30.6 LANDED (loop #151). The two WAL append paths now produce ONE
segment-boundary layout: `state.appendPGCompat` Path A predicts the emitted size
at the cursor and, on a crossing, re-lands at the boundary with the same pad
composition + `xl_prev` stamping Path B uses (pad and record in one contiguous
`writeAt`). Porting the shared rule exposed a second defect: a gap too small for
a record was left UNWRITTEN, which without `Config.Preallocate` leaves the
segment file short and stops replay (628 of 2068 records). Such a gap is now
zero-filled (PG: `AdvanceXLInsertBuffer` zeroes pages) and `onCrossSegment`
returns `padded bool` so only a real pad advances `prev`.
Design `docs/design/0131-0030`.

Gates: `go test ./internal/wal/` + `-race`, `./internal/initdb/ ./internal/storage/`,
units precommit, `RUNS=2 crashprobe30` **OVERALL: PASS (2 runs)** with both runs
exact on the atomicity invariant; pgbench smoke via the commit hook.

Next loop: banner still puts M0131 top. The only remaining S30 item is **S30.4**
(no checkpoint fires during ~180 MiB of WAL — decide explicitly: either goopg's
checkpointer never triggers on WAL volume, or the probe's settings disable it;
start from the checkpointer's trigger conditions and PG's `max_wal_size` /
`CheckpointerMain`). After that, the next unchecked M0131 item in the section.
