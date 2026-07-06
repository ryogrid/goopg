Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
corruption. 11th consecutive investigation loop. NO code landed this
loop (pure investigation) — but landed the first EMPIRICAL confirmation
in this 11-loop thread: built a diagnostic trace + enriched error,
reproduced `TestMultiWriterStress_M0055_Phase_C`'s "empty internal page"
failure 3x, and captured the exact byte state of the failing page.

Files touched this loop (all reverted before finishing, zero diff):
`internal/access/btree/btree.go` (temporary `debugTrace`/`debugDumpTraceFor`
ring buffer + 2 call sites + enriched descendToLeaf error, all marked
DEBUG-TRACE-TEMP), `internal/access/btree/multi_writer_stress_test.go`
(un-skipped + a `debugTraceLog = nil` reset line). `git diff --stat`
confirmed clean on both after `git checkout --`.

Key finding (THE breakthrough this loop): every failing page's dumped
state is `flags=0x0 lower=24 upper=8192 next=0 prev=0` — byte-for-byte
a BRAND NEW page straight out of `storage.InitPage`, never populated
with real B-tree content (no BTLeaf flag, Next/Prev both literal 0
instead of `storage.InvalidBlockNumber`=4294967295). This is NOT
"real content that got wiped" — confirmed by reproducing it on
`blk=1`, which had already been legitimately split-and-repopulated
5 separate times (198 items each) before a later reader found it
fully blank. This rules out a write-path logic bug (rightOpaque/
leftOpaque construction re-audited clean again) and points at the
READ side resolving to the WRONG PHYSICAL BUFFER for a tag it
believes it pinned correctly — i.e. `internal/storage/bufpool.go`'s
`p.bm` (bufmap) / `Pool.Pin`/`pinLoad`/`claimVictim`/`evictVictim`
slot-lifecycle machinery, NOT the btree.go split/root-lift code that
loops 8-10 exhaustively audited.

Ruled out this loop (static audit, all clean): `Pool.PinNew`
(bufpool.go:1028), `pinSlow`/`pinLoad` (bufpool.go:1194/1239),
`claimVictim`/`evictVictim` (bufpool.go:939/994), `relFile.extend`
(smgr.go:719 — correctly serialises block-number allocation under
`r.mu`, so no duplicate-blk-from-Extend race). Also corrected loop
9's factual error: `BTree.CompleteDeferredSplits` (btree.go:1724)
DOES exist and IS wired — it's just never called from the live
insert path (only maintenance/crash-recovery), so it can't explain
this test's failure; not a real gap in that sense.

Next step: recreate the exact instrumentation (full patch text is in
`.ralph/deferral_ledger.md`'s newest row, dated 2026-07-07, 11th loop
— copy verbatim, don't re-derive) and extend it ONE level down: in
`internal/storage/bufpool.go`, record per-slot-index the sequence of
(tag, gen, pointer identity of the returned `*storage.Slot`) on every
`Pool.Pin`/`PinNew` return. When `findChildBlockDirect` hits
`count==0`, cross-reference: does any OTHER trace entry show a
DIFFERENT slot index briefly holding the SAME tag around the same
time (a duplicate-mapping bug in `bufmap.go`'s Insert/Lookup/Delete,
which loop 10 "audited" but never actually exercised against this
specific failure signature)? That single check will confirm or
refute the leading hypothesis directly, rather than more static
reading of bufpool.go/bufmap.go.

Gates run this loop: `go build ./...` clean (both with and after
reverting instrumentation). `go test -count=1 ./internal/access/btree/...
./internal/amcheck/...` PASS post-revert. No executor/planner/codec
change, so no TPC-H spotcheck required. `make ralph-state-guard` run
before finalizing.

In-flight: none. Cheap repro for next loop: `BTREE_DEBUG_TRACE=1 go
test -run TestMultiWriterStress_M0055_Phase_C -count=500 -v
./internal/access/btree/...` (un-skip t.Skip at line ~40 first),
~180-220s wall time, observed 1-2 failures per 500-600 iterations,
zero setup/cleanup required beyond the test process itself. No
servers/data dirs/background processes started this loop. Separate
live nightly CI batch (`ci/batch/run-nightly.sh`) not touched.
