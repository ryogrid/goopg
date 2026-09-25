# M0142-0005b — streamed NL index-probe executor (leaf-grain `ScanCursor`), then re-adjudicate `indexProbeCostMultiplier`

Task: `.ralph/fix_plan.md` **M0142-0005b** (banner item 6, child of
M0142-0005). Kind: impl. Parent: M0142-0005. Filed by the B8
re-measurement (`m0142-0005-b8-index-probe-mult-reverify.md`), which
proved `indexProbeCostMultiplier = 2.0` is still load-bearing and
corpus-tensed: it suppresses PG-matching NL+`Index Scan` probes on TPC-DS
(`scan-type` blockers 51→59 between the arms) while protecting TPC-H
parity (mult=1 slides Q9/Q10/Q14 to NL+index where PG hashes). The knob's
own comment (`internal/optimizer/cost_funcs.go:1108-1135`) states the
mechanism it compensates — "goopg materialises the whole TID list eagerly
per probe (ch. 06 §5)" — and explicitly anticipates this task ("the next
recalibration after the NL-probe execution work this comment describes").

Status: accepted (landed 2026-09-19). The streamed leaf-grain cursor is
in; the two-arm re-measure reproduced B8 byte-for-byte — executor
laziness cannot change plan choice — so `indexProbeCostMultiplier` stays
at 2.0 with its mechanism comment corrected, and the residual (probe-vs-
hash relative pricing) is filed as M0142-0005c (recon).

## PG oracle

`postgres/src/backend/executor/nodeIndexscan.c` — `IndexNext` calls
`index_getnext_tid` (`indexam.c`), which returns ONE heap TID per call
from a resumable index-scan position (`IndexScanDesc.so->currPos`; the
btree keeps its place across calls — `_bt_next`/`_bt_steppage`,
`nbtree/nbtsearch.c`), then `heap_fetch` that one tuple. Nothing
materialises the match set; a probe that stops early has paid for exactly
the leaf pages it read. The cost model's `cost_index`
(`optimizer/path/costsize.c`) prices this per-tuple model — it assumes the
caller can stop mid-scan without paying for the remainder.

## The goopg gap

`indexScanOp.Rescan` (`internal/executor/operators_index.go`) runs
`RangeScanWithPosLeafFilter` **to completion** inside Rescan, collecting
every matching TID into `o.tids` (and `o.poss` for the C3-S2 kill list)
before the first `Next()` can run. `Next()` then heap-fetches lazily
(M0092-0001 already fixed that half). So per probe the eager part is the
full leaf-chain walk + TID collection: bounded work the consumer may never
need (semi/anti inner stops after the first qualifying row, LIMIT stops
the outer, an inner `Filter:`/residual can end the match early). Under an
NLI over a large outer this per-probe overhead is what made PG-priced NL
plans ruinous — the knob's documented reason to exist.

## Design — leaf-grain resumable cursor

A per-tuple btree cursor in goopg needs intra-leaf position bookkeeping
that survives page splits between calls (PG solves this with a held
buffer pin + key-comparison repositioning in `_bt_steppage`). goopg's
existing scan already accepts a leaf-boundary race window (unpin N → pin
next); a per-tuple cursor would widen that window to every tuple. The
chosen middle ground keeps the correctness envelope unchanged while
capturing the real laziness win:

**`nbtree.ScanCursor`** — a resumable, leaf-grain range scan:

- `NewScanCursor(lo, hi, loExclusive, hiExclusive, leafFilter)` performs
  the one-time `descendToLeaf` and stores the walk state (next leaf block
  + bounds). No pin is held across calls.
- `Next(fn)` advances over exactly one leaf page: pin, the existing
  `keyExceedsHighKey` recovery skip and `leafFilter` admission, then the
  identical per-item lo/hi bound checks, invoking `fn` per in-range
  entry; unpin; record `op.Next` as the next leaf. Returns `ok=false`
  only when the leaf chain is exhausted.
- The per-leaf item loop is **extracted unchanged** from
  `rangeScanPosLeaf` into a shared `scanLeafItems` helper so the cursor
  and the existing eager API apply byte-identical bound semantics
  (posting items, `compareHigh` vs `compare` for exclusive bounds,
  M0134-0001 S4 rules, `ScanPos` capture for the kill list).
- Split/race exposure is IDENTICAL to today's scan: the only unpinned
  window is the leaf boundary (`unpin cur` → `pin op.Next`), which the
  eager scan already has. Storing only the next *block* (read fresh from
  `op.Next` each call) means a split that inserted a new right sibling is
  discovered exactly as today.

**`indexScanOp` rewiring** — `o.tids`/`o.poss`/`o.idx` keep their shape
but become a per-leaf *batch* instead of the whole match set:

- `Rescan` resolves the probe bounds exactly as today, then constructs
  the cursor — no leaf pages are read until the first `Next()`.
- `Next()` pulls the next leaf batch (`tids[:0]`/`poss[:0]` refill) only
  when `idx` reaches the batch end; EOF when the cursor exhausts.
- `rescanSAOP` evaluates every element key eagerly (same eval timing as
  today, same eager per-element bucket SIREADs) but collects TIDs through
  a lazy chain of per-element cursors — an element's descent and leaf
  reads happen only when the chain reaches it, and the `seen` dedup map
  persists across the pull loop.
- Kill list (`C3-S2`/`S3`): unchanged — `poss` stays parallel to the
  current batch, `flushKills` still runs at Rescan/Close.

**Deferred SSI gap-lock decision.** `ssiRecordIndexScanGapLock` currently
runs at the end of Rescan reading `len(o.tids) > 0` ("did the probe match
≥1 index entry"). Under laziness that answer is unknowable at Rescan
time, so the decision moves to a `finalizeIndexScanSSI` once-per-scan
hook fired at scan exhaustion, the next `Rescan`, or `Close` —
whichever comes first — keyed on `sawTID` (≥1 TID pulled). SIREAD locks
are predicate markers valid for the whole transaction, so registering at
exhaustion/Close is still strictly before commit and preserves the
rw-edge semantics. The one behavioural shift is conservative: a scan
abandoned before its first TID (never happen under today's NLI driver —
the inner is always Next-driven — but possible for a cancelled query)
records the relation-grain lock where today's code saw `len(tids)>0`
matches deeper in the chain and skipped it — an over-approximation, never
an under-lock.

## Why leaf-grain, honestly

For a unique-key point probe (the canonical NLI inner) a leaf holds the
whole match set anyway, so per-probe work is near-identical before and
after — descent + one leaf + heap fetch. The genuine wins are (a) probes
whose match set spans multiple leaves where the consumer stops early
(semi/anti inners, LIMIT, residual-filtered probes), (b) the `tids`/`poss`
slice growth for large match sets, and (c) SAOP elements never reached.
This change is landed because it is the PG-faithful execution model —
the multiplier's own comment names it as the exit — not because it is
expected to make point probes dramatically cheaper.

## Then: the knob re-adjudication

Executor speed does not change the planner's arithmetic, so B8's mult=1
capture already predicts the plan-level answer: at `GOOPG_INDEX_PROBE_MULT=1`
TPC-H Q9/Q10/Q14 still pick NL+index where PG hashes — **if** nothing else
in the cost model moved. The task's contract is therefore:

1. Land the streamed probe.
2. Re-run the two-arm B8 measurement on the post-change binary (same
   corpus, same private-clone procedure).
3. If TPC-H Q9/Q10/Q14 keep PG's hash joins AND TPC-DS keeps the mult=1
   scan-type gains → retire the knob (`indexProbeMultCalibrated = 1.0`,
   delete the flag plumbing) — Movement: yes, TPC-DS `scan-type` −8.
4. If the TPC-H divergence persists (expected) → the residual is a real
   cost-model gap (probe cost vs hash cost relative pricing, not
   executor speed): the knob stays, its comment is corrected to name the
   true mechanism, and the residual is filed as its own recon task.

## Result (2026-09-19, binary sha256 `d26896dbef17`)

Branch 4 — the prediction held exactly:

- **Post-change captures are byte-identical to B8's** (shape-stripped
  diff: 0 changed lines on both TPC-H arms; TPC-DS arms differ only in
  capture headers and temp file names). The streamed executor changes
  per-probe runtime cost, which the cost model does not observe.
- TPC-DS SF0.25 (`pg-plan-parity-diff.py` vs `bench/tpcds/plans-pg/`):
  mult=1 → match=2, scan-type=51, join-order=88, qual-placement=24;
  mult=2 → match=2, scan-type=59, join-order=91, qual-placement=20 —
  identical to B8's tallies on both arms.
- TPC-H SF=1 (vs `bench/tpch/plans-pg/`): both arms match=2 (Q11, Q13).
  At mult=1 Q9/Q10/Q14 still choose `Nested Loop`+`Index Scan` probes
  (`partsupp_part_fkidx`, `customer_pk`, `part_pk`) where PG 18.3 uses
  Hash Joins; mult=2 keeps the hash joins.

**Verdict:** the multiplier is not compensating executor overhead — with
the eager TID list gone, the corpus-tensed plan divergence is unchanged.
It is masking a cost-model relative-pricing gap. `indexProbeCostMultiplier`
stays at 2.0; its comment now names the corrected mechanism; the residual
is filed as M0142-0005c (recon). Movement: none — plan parity is unchanged
(the TPC-DS scan-type −8 only materialises at mult=1, which TPC-H still
forbids); the executor now runs PG's streaming probe model, which is the
fidelity improvement this task exists to land.

## Gates

`go build ./...`; `go test ./internal/access/nbtree/...` +
`./internal/executor/...` (index_scan / saop / nli / rescan suites);
`go test -race ./internal/executor/` (cursor state is per-operator, no
shared state, but the scan path is concurrency-adjacent);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`scripts/tpch-spotcheck.sh`; `scripts/tpcds-sf025-regression.sh sweep`;
`scripts/tpch-acceptance-arm.sh`; then the B8-protocol two-arm mult=1/2
parity captures.

## Implementation notes (as landed)

- `nbtree.ScanCursor` + `NewScanCursor` + `Next(fn)`: leaf-grain resumable
  range scan in `internal/access/nbtree/btree.go`; the per-leaf item loop
  was extracted unchanged into `scanLeafItems`, shared by the cursor and
  the eager `rangeScanPosLeaf` (byte-identical bound semantics — posting
  items, `compareHigh` exclusive-bound rules, `ScanPos` capture).
- `indexScanOp`: `tids`/`poss` are now per-leaf batches refilled by
  `nextLeafBatch()` (called from `Next()` when `idx` reaches batch end);
  `scanAppendEntry` is the cursor callback. SAOP became a lazy chain of
  per-element cursors (`saopBounds`/`saopIdx`/`saopSeen`) — bounds and
  hash-bucket SIREADs still eager, descents and leaf reads lazy.
- SSI gap-lock decision deferred to `finalizeIndexScanSSI()` — once per
  scan, at exhaustion, next `Rescan`, or `Close`, keyed on `sawTID`
  (replacing `len(tids)>0`, which is no longer knowable at Rescan end).
  `ssiDone` starts true (nothing owed before the first scan).
- NLI/memoize contracts unchanged: `Rescan`+`Next` pull-driven as before;
  Gather leaf ownership rides the cursor's `leafFilter`.
- New tests `internal/access/nbtree/scan_cursor_test.go`: cursor-vs-eager
  stream equality (keys+TIDs+ScanPos, multi-leaf), early-stop exhaustion,
  exclusive bounds, empty/degenerate scans, leaf-filter partitioning.

## Artefacts

- `analysis/m0142/m0142-0005b-tpcds-mult1.txt`,
  `analysis/m0142/m0142-0005b-tpcds-mult2.txt`
- `analysis/m0142/m0142-0005b-tpch-mult1.txt`,
  `analysis/m0142/m0142-0005b-tpch-mult2.txt`
- Servers: `tmp/m0142-0005-b8/data-sf025` (:5533, reused B8 clone),
  `tmp/goopg-spotcheck-tpch-data` (:5534); binary `tmp/m0142-0005b/goopg`
  sha256 `d26896dbef17…`; env verified via `/proc/<pid>/environ`.
- Gates: units PASS; `go test -race ./internal/executor/` PASS;
  tpch-spotcheck PASS (Q12=2/Q13=34, stamped); tpcds-sf025 sweep PASS=96
  MISMATCH=0 plan-shapes 99/99 (stamped); tpch-acceptance-arm PASS 24/24
  value-MATCH vs baseline (stamped).
