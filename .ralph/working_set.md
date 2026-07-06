Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
"item length mismatch keyLen=9 total=37" recurrence investigation
(NOT resolved; investigation-only loop, no functional change landed).

Files: none changed in the final commit besides `.ralph/fix_plan.md` and
`.ralph/deferral_ledger.md` (bookkeeping). Temporarily edited and REVERTED
(not committed): `internal/access/btree/multi_writer_stress_test.go`
(un-skipped `TestMultiWriterStress_M0055_Phase_C` to get a fast repro),
`internal/access/btree/btree.go` (temporary debug `fmt.Errorf` in
`descendToLeaf`, ~line 1321, printing cur/level/isRoot/metaRoot).

Key symbols: `internal/storage/bufpool.go` `Pool.PinNew` (line 1029-1104,
prime suspect — publishes bm tag + slotValidBit at lines 1085/1088 BEFORE
caller's `.Lock()`+populate), `claimVictim`/`evictVictim`/`tryPinSlot`
(same file, ~880-1025). `internal/access/btree/btree.go`
`pinNewOrRecycled` (646), `createNewRoot` (1819), `insertIntoBlock` (1422),
`descendToLeaf` (1272), `findChildBlockDirect` (1221, raises "btree: empty
internal page" at line 1227).

Hypothesis/Findings: confirmed nightly recurrence is the same error CLASS
as the 2026-06-26 M0118-0130 fix (recycled-page zeroing under lock) but
NOT the same specific bug — that fix is intact and correct (verified by
re-reading it). Found a FAST in-process repro reusing the already-skipped
`TestMultiWriterStress_M0055_Phase_C`: remove its `t.Skip` line and loop
`go test -run TestMultiWriterStress_M0055_Phase_C -count=1
./internal/access/btree/...` — reproduces a sibling "btree: empty internal
page" error ~2/40 single-process runs (seconds each). Debug capture showed
the failing page's opaque = Go zero value (level=0, isRoot=false) while
meta.Root pointed at a different block — i.e. a reader reached a page that
went through `storage.InitPage` (inside `Pool.PinNew`) but not yet the
btree layer's item-population step. Verified via Go memory-model
happens-before chaining through per-page `contentMu` mutexes that ordinary
split→parent-downlink content visibility IS correctly ordered for readers
— ruled out plain torn-read-via-btree-locks as the mechanism. Root cause
therefore lives in `Pool.PinNew`'s bufmap/slot-state publish ordering
(publishes valid+bm-visible BEFORE caller populates content) or in
claimVictim/evictVictim/tryPinSlot's generation bookkeeping — not
conclusively isolated further this loop. Full detail + exact resume
instructions: `.ralph/deferral_ledger.md`, last row (M-NIGHTLY,
2026-07-06), and `.ralph/fix_plan.md`'s pgbench/nightly item (2026-07-06
update paragraph).

Next step: instrument `internal/storage/bufpool.go`'s `bm.Insert`/
`bm.Delete`/`bm.Lookup`/`claimVictim`/`evictVictim` with tag+slotIdx+gen
logging, re-run the fast repro (`TestMultiWriterStress_M0055_Phase_C`,
t.Skip removed) in a loop until it fails, and find the exact moment a
reader's Pin(tag) resolves to a slot still mid-PinNew-publish for a
DIFFERENT tag. Do NOT attempt a fix without this — buffer-pool eviction
concurrency is a distinct, high-blast-radius subsystem (ledger item
M0118-0130 blocker (4)); a rushed fix here previously caused a different
panic class (see btree.go:601-613 comment on the reverted splitMu-removal
attempt). Do NOT re-enable `TestMultiWriterStress_M0055_Phase_C` as a real
gate until the flake itself is fixed — it will break CI if unskipped.

Gates run this loop: go build ./... clean; go test
./internal/access/btree/... ./internal/storage/... PASS (baseline,
unchanged); make ralph-state-guard OK. No executor/planner/codec changes
this loop, so no TPC-H spotcheck required (investigation + docs only).

In-flight: none — all temporary debug/skip edits to
internal/access/btree/{btree.go,multi_writer_stress_test.go} were reverted
via `git checkout --` before this loop ended; only `.ralph/fix_plan.md` and
`.ralph/deferral_ledger.md` carry real changes, both committed.
