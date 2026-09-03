# EX0-04 Design — Per-operator timing harness

Item: `TODO_EXECUTOR.md` EX0-04 (gate: slices published for the three
witness shapes; pin `changed=0`). Status: design for review.

## 1. What the harness is (and is not)

A checked-in script `bench/tpch/profile_slices.sh` + a shares doc, NOT a
Go test (Go tests cannot hold pprof/GC/server-age steady) and NOT new
in-operator timers (no *phase-subdivision* timers exist — only whole-node
`instrumentedOp` timing, whole-build `BuildTimeNs`, and top-level
Planning/Execution wallclock; sortOp/spill/scan paths have no dedicated
phase timers, so subdivision is via pprof symbols + EXPLAIN node times).
The harness makes the EXISTING sources repeatable: fixed inputs, fixed
collection, fixed symbol→slice mapping, fixed validation.

## 2. Sources and mapping

- Node times: `EXPLAIN (ANALYZE, TIMING ON)` per operator (nested
  stopwatch — exclusive time = node total minus children; coarse
  cross-check only).
- CPU slices: pprof `cpu` STACK-filtered (`-traces`, not single-edge
  sums) cum attribution over the six symbol sets — single edges cannot
  separate `compareDatum` (~15+ call sites: prefilter-compare and
  probe-compare both route through `evalFastExpr`; `evalBinary`-routed
  residual compare is indistinguishable from filter compare at any
  single edge). Stacks must show the grandparent frames
  (`evalPrefilter` vs `joinPredicateMatchSlot`/`evalHashKey*` vs
  `lessKeyVals`); the `evalBinary` ambiguity is enumerated per slice,
  not wished away. Symbol sets: decode (`decodeRowRangeInfo`,
  `decodePhysicalPGValueLowered`, `nodes.NumericInt64FromStoredPayload`
  — may inline into its caller, harmless —
  `storage.pageChecksumBlock`); prefilter (`evalFastExpr` under
  `evalPrefilter` + the `evalPrefilter` edge itself);
  clone (alloc-side only); join-probe (`evalHashKey*`,
  `joinPredicateMatch*` stacks + `compareDatum`-under-probe stacks);
  sort-compare (`evalSortKeyValue` + `lessKeyVals→compareDatum`
  stacks — verify the `lessKeyVals` frame is present, `compareDatum`
  is too large to inline); spill-write (`spillWriter` + file/IO
  presence). `runtime.futex`/`usleep` ALWAYS excluded with the excluded
  total stated.
- Alloc slices: pprof heap `-base` deltas (flat for alloc per take6 §1).

## 3. Validation rule (the item's falsifiability)

The take7 split (95.3 : 4.2, ~0.33 pp) is HISTORICAL REFERENCE ONLY: it
was cut on a parallel pre-take7 tree where the prefilter was still
interpreted; the current tree compiles+folds the prefilter, so the
ratio necessarily shifted, and parallel→serial changes the pp
denominator. Before gating, re-cut the anchor with ONE fresh run on the
current tree, serial S-cold — that re-pinned value is the gate, take7
is the sanity bound.

Gate, on Q6 (fresh server, serial, S-cold, 40 s window, ×3 runs):
primary — the self-normalizing `filterOp`-edge /
(`prefilter`-edge + `filterOp`-edge) ratio within ±1 pp absolute of the
re-pinned anchor; secondary — pp-of-total as mean-of-3 within ±0.25 pp
(single 40 s runs carry ~±0.09 pp Poisson noise alone). AND Q6
join/sort/spill slices ~0 (null control). Q9 probe slice > 0 and stable
±10% relative. Failure = harness or mapping bug, never a perf claim.

## 4. Slice provenance (re-collect — the old corpus cannot be reused)

The `bench/tpch/pprof/` `q{9,4,7,13}_cpu.pb.gz` corpus (mtime
2026-08-08) PREDATES take7 (~2026-08-30, compiled+folded prefilter on
every scan — CPU attribution changed on every filtered shape), is
git-ignored (unpinned local files), and its runner logs (mtime
2026-07-24) are two weeks older than the profiles they supposedly
describe. Deriving slices from it is void. All four shapes are
RE-COLLECTED under this harness (single arm, fresh server; Q4 ~285 s,
Q7 ~160 s, Q13 ~110 s, Q9 ~31 s per the old runner logs — budget ~10
min query time + overhead, no A/B). Each slice states its source
profile + collection commit. The node-time cross-check arm runs
SERIAL ONLY (under Gather, worker time lives in EX0-03b tables outside
leader totals — specify serial or leader-only-with-stated-understatement).
- This item also discharges 13 §2.2's "decide G-EX6's type-by-type
  remainder list empirically": the decode slice is cut per type family
  (numeric / text / date / TOAST-pointer) from the Q6 + Q1-class
  profiles, producing the remainder list EX1 consensus consumes.
- Outputs per shape: `cpu.pb.gz` (or source pointer), `slices.txt` (six
  shares + excluded total + node-time cross-check), `explain-analyze.txt`.

## 5. Pin and scope guards

- The harness moves no code under measurement (new files only:
  script + shares doc + TODO line) → `git diff --stat` shows
  bench/docs-only (no `internal/…`), AND the item closes with plan-gate
  green. Value-identity assertion in the script (pinned-value `diff`
  with `set -e` — stricter than `ab.sh`'s eyeball `*.value` files) is a
  harness self-check against wrong query text/silent drift, NOT the pin.
- No per-item timing claims; no new timers; no query-specific forcing
  (shapes, not queries — Q6/Q9/Q4/Q7/Q13 are the TODO-named witnesses).
  The one hot-path addition in range (EX0-03c's `peakBytes` branch in
  `sortOp.Open`'s pull loop) is negligible vs sampling noise — stated,
  not ignored.
