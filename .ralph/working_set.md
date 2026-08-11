(idle — nothing in flight)

Loop #134 worked **M0131-S30.3** (diagnosis; probe landed, no engine fix yet).

**Built the probe the previous loop specified** — `internal/storage/pageident_probe.go`
(`GOOPG_PAGEIDENT_PROBE=1`), driver `analysis/pageident_probe.sh`. It reports
`PAGEIDENT-REGRESS` (a heap page under a tag has FEWER line pointers than the
high-water mark for that tag = buffer tag/content aliasing) and
`PAGEIDENT-REEXTEND` (extend hands out a block that already carried tuples).
Observes at every disk read, disk write, pre-eviction flush, and in
`tryApplyHOTUpdate` under the content lock. Heap-only filter is
`pd_special == BlockSize` — WITHOUT it btree splits give 336 benign hits
(`lp=205 high=407`); don't re-discover that.

**Both S30.3 prime suspects are REFUTED — do not re-test:**
1. buffer-pool tag/content aliasing — ZERO hits in a clean 45 s scale-5/16-client
   run AND in a `crashprobe30` run that DID lose rows (497287/500000, 2713 missing).
2. `IsNew`-driven silent `PageInit` past a shortened file — no such path:
   `relFile.readBlock` returns `ErrShortRead` for `blk >= nblocks` and `pinLoad`
   propagates the error (never publishes a zero page).

**New defect found and filed as M0131-S30.5:** `tryApplyHOTUpdate`'s orphan-cleanup
arm (`operators_storage.go:3746`) calls `storage.PageRemoveHeapTuple` and emits NO
WAL — and that function SHRINKS `pd_lower` when the removed slot is last
(`heap.go:697-699`). Slot is already dirty from the logged prune, so it reaches
disk and replay rebuilds a larger page. One LP per occurrence, so it is not the
`185 -> 1` root cause.

Next step (S30.3): fire `PageIdentityObserve` inside `PageRemoveHeapTuple`, and
assert at `markHeapHotUpdateDirty` that the emitted `new_off` equals both `newSlot`
and `PageLinePointerCount(page)` — catch an emit-time inconsistency before the
crash hides it. If clean, walk block 130's records from the START of the stream.

Repro still preserved: `cp -a /tmp/s30_3_repro/data /tmp/try` → goopg refuses to
start in ~35 s (251 MB; copy somewhere durable if /tmp is at risk).

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

Gates run: units suite PASS; `make ralph-state-guard` OK after self-repair;
pgbench smoke via the commit hook. Design `docs/design/0131-0021` §Update 2026-08-11.

In-flight: none.
