# EX1-02b Design — Bound threading for index/bitmap/IOS paths

Item: `TODO_EXECUTOR.md` EX1-02b (new line this commit; gate: values +
pin + index-heavy coverage). Status: design for review. EX1-02 covered
SeqScan consumers under joins/projects; these paths still full-deform.

## 1. Scope (three decode sites + NLI inner)

- `indexScanOp` heap fetch (`operators_index.go:611`
  `DecodeHeapTupleRowInto`): add `deformBound` field (mirror
  `seqScanOp`), range-decode (decompose tuple → Data/Bitmap/natts
  from Infomask2, call `DecodeRowRangeIntoMctxPGTupleStyled`
  `codec.go:1331` by name — there is NO HeapTuple range helper) +
  `poisonDeformTail`. Bound = union(parentWalk, CondRefs). Index key
  cols come from the index tuple, never the heap deform
  (`outerSlot` eval) — unioning them is belt-and-braces
  over-widening, not load-bearing; key EXPRS must NOT fold into the
  inner bound at face value (outer indexes as inner indexes =
  coordinate error, widening-only but wrong).
- NLI inner rescans (`operators_index.go:358-374` per-outer-row
  re-probe): consumer column SET is plan-static (Cond + keys + parent
  bound mapped to inner-output space); per-call consumer VALUES vary
  but the bound does not move per call — thread once at build, same
  field, no per-Rescan work. Thread into the inner at
  `executor.go:182-202` (incl. Memoize wrap); outer keeps the
  EX1-02 left-side rule.
- `bitmapHeapScanOp` (`operators_bitmap.go:1033-1038`, callers
  `:691,:749`): same field + range decode. Recheck interplay:
  `BitmapQual` recheck cols + `Cond` cols (`:695-718`) are genuine
  consumers — fold them FIRST, then union the parent bound; if either
  declines, the whole bound declines (re-widen, never narrow past a
  recheck reader). Inner bitmap builds (`:408,887,968`) take the
  EX0-03b nil-scope-equivalent: no bound (they build TID sets, not
  rows — nothing to narrow).
- `indexOnlyScanOp`: NO plan walk needed (consumer = `Covered` list).
  Two local fixes: key-loop early stop (`:629-643` — stop decoding
  after the highest covered key ordinal); heap-fallback subset
  decode (`:788-812`). If   `Covered` can be non-contiguous (index-column order, e.g. index
  (a,c) → heap ordinals {0,2}): preferred fix is range-decode
  `[0, maxCovered+1]` (over-decode gaps, zero codec change — decode
  must still walk THROUGH gaps to higher ordinals, offsets are
  sequential); a subset helper only if the gap cost ever matters.

## 2. Safety arguments (path-specific)

- IndexScan Cond + keys: Cond evaluated per fetched row (leaf-local
  Filter equivalent); index key cols are read by the descent itself
  (covered by threading the bound as union WITH key cols — keys are
  always inside: index descent reads them before the heap fetch).
  State precisely: bound = union(parentWalk, CondRefs, keyCols).
- Bitmap recheck: lossy pages re-evaluate BitmapQual on the heap row —
  the recheck reader is the auth consumer most likely to be forgotten;
  the design makes it FIRST, not an afterthought, and any decline in
  the recheck walk fails the whole bound.
- IOS Covered: output-narrow already proven; decode-stop only skips
  work AFTER the last covered column (prefix property reused).
- Rescan: bound is build-time static; Rescan changes bindings, not
  columns. Poison runs over re-probe paths prove it.
- DML/EPQ/lockrows/unique-check/FK/catalog/DDL/VACUUM/ANALYZE/COPY:
  untouched, full by correctness (EX1-02 recon table).

## 3. Coverage (index paths need more than TPC-H)

TPC-H SF=1 is seq-scan dominated — TPC-H values alone cannot gate
index narrowing. Required: poison-run unit tests over Rescan
re-probe + lossy-recheck + Cond paths; existing btree/indexonly
suites green; TPC-DS sweep (index-heavier) PASS=95 as the values
gate; plan-gate 22/22. Q4 (NLI probe 59%) is the witness that must
move or be explained.

## 4. Verification (gate)

- Unit: bound threading per path (index/bitmap/IOS/NLI-inner),
  recheck-widen, Cond-union, key-col inclusion, poison runs.
- Values: tpch-runner -diff 24/24 + TPC-DS PASS=95 + plan-gate.
- Timing + alloc on Q6 (no-op expected — no index in Q6 chain;
  assert unchanged) + Q4 (moves or explained) + Q13.
