# TPC-H Status — Phase 9 (M0077 close)

**Date:** 2026-05-11
**Branch:** `try-codex` at HEAD `5d8bd43`
**Predecessor:** `docs/handover/2026-05-10-tpch-status-phase8.md` (M0076 close)
**Milestone closed:** M0077 (Q5 planner fix — 4-slice planner refactor per
`docs/design/fix-for-q5/`)

## TL;DR

Q5 was unblocked. The 4-slice landing per the design bundle worked exactly
as predicted: Slices A+B+C alone unlocked Q5 (313 s, 5 rows); Slice D's
anchored synthesis cut Q5 to **26 s**, well under the milestone's "best
effort < 60 s" target. Q9's mode-1 7-row baseline graduated to the
canonical SF=1 175-row result as a side effect of Slice C's build-side-
aware cost (no behavioural rule change to Q9). All structural row-count
gates pass. The "should stay identical" plan-diff set matched the M0076
baseline through every slice.

## Sub-milestone landing summary

| # | Sub-milestone | Commit | Status |
| - | ------------- | ------ | ------ |
| 0001 | Slice A — local predicate partition + leaf-Filter attachment + chained-NLI rebind robustness | `174cb90` | accepted |
| 0002 | Slice B — `baseRelInfo` post-filter row estimates + `selectivityEstimate.reliable` + DP row plumbing | `71eeba3` | accepted |
| 0003 | Slice C — build-side-aware 3-part hash-join cost (output*1 + build*4 + probe*1) | `da260d1` | accepted |
| 0004 | Slice D — anchored equality synthesis (`inferAnchoredEqualities`) | `5d8bd43` | accepted |
| 0005 | Final 22-q sweep + Phase 9 handover | this commit | in progress |

## Full 22-q sweep at M0077-final (HEAD `5d8bd43`)

```
Q1:  OK  21.80s  4 rows
Q2:  OK  38.19s  470 rows
Q3:  OK  20.69s  11462 rows
Q4:  OK 155.91s  5 rows
Q5:  OK  16.77s  5 rows         ← canonical SF=1 (was cancel @1100s in M0076)
Q6:  OK  16.32s  1 row
Q7:  OK  95.30s  4 rows
Q8:  OK 131.98s  2 rows
Q9:  OK  66.26s  175 rows       ← canonical SF=1 (was 7-row mode-1 baseline)
Q10: OK  24.96s  20574 rows
Q11: OK   3.17s  1142 rows
Q12: OK  85.81s  2 rows
Q13: OK  63.29s  35 rows
Q14: OK  19.49s  1 row
Q15a-VIEWBODY: OK 17.33s  10000 rows
Q15b-MAIN:     OK 32.18s  1 row
Q16: OK   5.00s  18170 rows
Q17: OK  48.08s  1 row
Q18: OK  36.21s  11 rows
Q19: OK  73.03s  1 row
Q20: OK  16.40s  99 rows         ← known dataset variance vs canonical ~186
Q21: OK 350.47s  381 rows
Q22: OK  60.55s  7 rows
```

**Every query executed without error; every gated row count holds.**
Q20's 99-vs-186 gap is confirmed dataset variance (carry from M0076);
the dataset re-generation is out of scope for M0077 (parked in M0078+).

## Per-slice timing on the focused gate (`--queries=3,5,8,9,12,13,21,22`)

All numbers are SF=1 against `bench/tpch/runtime_goopg/data` with
`--per-query-timeout=620s --cancel-after=600s`. Cells show
`elapsed=Xs rows=Y` (rows in **bold** when the row-count differs from
the prior slice).

| Q  | M0076-baseline | Slice A | Slice B | Slice C | **Slice D** |
| -- | -------------- | ------- | ------- | ------- | ----------- |
| 3  | OK 11462       | 36s 11462 | 36s 11462 | 36s 11462 | **35s 11462** |
| 5  | cancel @1100s  | cancel @600s | cancel @600s | 313s 5  | **26s 5**     |
| 8  | (had MHJ)      | 421s 2  | 25s 2   | 147s 2  | **149s 2**    |
| 9  | 7 mode-1       | 45s 7   | 325s 7  | 82s **175** | **80s 175**   |
| 12 |                | 91s 2   | 95s 2   | 91s 2   | **92s 2**     |
| 13 |                | 62s 35  | 63s 35  | 61s 35  | **62s 35**    |
| 21 |                | 382s 381 | 387s 381 | 377s 381 | **376s 381**  |
| 22 |                | 59s 7   | 60s 7   | 59s 7   | **60s 7**     |

### Notable wall-time movements

- **Q5: cancel → 26 s** (M0077-final). Slice C alone (with B's filtered
  rowcounts) was enough to land a binary-tree plan that completes; Slice D
  cut Q5 a further 12× by adding the `c_nationkey = n_nationkey` anchored
  edge so the planner could place customer on the filtered nation side
  directly.
- **Q8: 421 s → 149 s** (M0077-final). Slice A's leaf-local filter
  attachment + chained-NLI rebind robustness landed Q8's first-time
  green; Slice B briefly hit 25 s (an overshoot from the still-single-
  output cost formula picking an aggressive plan); Slice C settled on
  149 s with a more balanced plan choice.
- **Q9: row count 7 → 175** (canonical SF=1). With Slice B's filtered
  rowcounts threaded through the DP and Slice C's build-side-aware cost,
  the planner stopped picking the chained-NLI shape that produced only
  7 rows under the prior mode-1 baseline. No anti-rule was needed —
  the cost formula naturally moved the planner to the correct plan.
  Slice D preserved this result.
- **Q21: flat at ~380 s through every slice.** Q21's outer FROM has 4
  tables, so Slice A's `shouldAttachBeforeMHJ` gate never fires for it;
  the chained-NLI / Anti shape that Slice A's `Filter.LeafLocal` flag
  doesn't disturb continues to dominate.

## Plan-diff vs `m0076-baseline-ffc3429`

"Should stay identical" set (Q1, Q4, Q6, Q14, Q16, Q17, Q19, Q20, Q22):
**all MATCH** through every slice.

"May change" set (Q2, Q3, Q5, Q7, Q8, Q9, Q10, Q11, Q12, Q13, Q18, Q21):
diffs limited to Q2, Q5, Q7, Q8, Q9 — every other query in the may-
change set still matches the baseline.

## Slice D cost-model interaction with M0076-0004

`inferredEdgePenalty` (M0076-0004) continues to multiply the 3-part cost
when an edge is tagged `isInferred=true`. Slice D feeds anchored
synthesised conjuncts through `buildJoinGraph(... inferredCount=N ...)`
so the synthesised edges inherit the penalty multiplier. The penalty
remains useful as a tiebreaker — it is no longer the sole defence
against bad inferred edges (the anchor rule now handles that
structurally), but it does keep an explicit `c_nationkey = n_nationkey`
in a hypothetical future query strictly preferred over the synthesised
equivalent of identical 3-part cost.

## Catalog persistence change shipped with Slice A

The takeover in mid-Slice-A discovered that `SmallDimension` was not
persisted across `Snapshot()` / `Restore()`. Without persistence the live
restarted bench server lost the hint, `shouldAttachBeforeMHJ` declined
to fire, and Q5 silently regressed to its pre-Slice-A shape on every
fresh server start. The fix:

- `internal/catalog/persist.go`: `TableEntry.SmallDimension` field plus
  `isKnownSmallDimensionName(name)` legacy fallback so existing
  snapshots regain the hint for `region` / `nation` on restore.
- `internal/catalog/persist_test.go`:
  `TestSnapshotPreservesSmallDimension` and
  `TestRestoreLegacySnapshotReappliesSmallDimension`.

This change is small but **load-bearing for every Slice A onwards**;
without it the live planner sees no SmallDim bindings and the entire
M0077 chain becomes a no-op on a freshly-restarted cluster.

## Chained-NLI rebind robustness shipped with Slice A

Slice A's MHJ-fragmentation surfaced a secondary issue in Q8: the
chained `NestedLoop(NestedLoop(..., customer_pk), nation_pk)` shape
hit `pq: column "c_nationkey" is not numeric at runtime (42804)`
because the probe-key `cr.Index` pointed at `n_name` (char) in a stale
NLI schema. Two related fixes landed with Slice A:

1. **`outerIsJoin` schema-refresh** in `tryBuildNLI`: when the NLI
   outer is a binary `*Join`, call `reresolveJoinByName` to refresh
   `outerJoin.schema` from the current Left/Right outputs before
   binding probe keys. The takeover wrote the first version; this
   commit kept it.
2. **Type-mismatch override** for `outerIsNLI`: when the slot at the
   stale `cr.Index` carries a type incompatible with `cr.Type`, bypass
   the M0075-0002 selectivity guard and force the rebind. Q9's
   "preserve stale index" mode (which avoided rebinding to a high-
   cardinality column) still works because Q9's stale slot has the
   right runtime type even when the schema annotation diverges.
   Implemented as `origTypeMatches(cr, outerSchema)` in
   `internal/planner/nl_index_join_selectivity.go`; pinned by
   `TestOrigTypeMatchesDetectsRuntimeTypeMismatch`.

## Carry-forward to M0078

- **Q5 wall-time floor < 10 s.** Q5 at 26 s is dominated by per-row
  CPU eval (filterOp, hash join probe). The M0076-0002b filterOp
  predicate batch wiring (still parked behind the arena retention
  audit) is the highest-leverage executor-side win.
- **Q8 wall time of 149 s.** Q8 has 8 tables and Slice C's 3-part
  cost picked a slightly different plan than Slice B's 25-s overshoot.
  The 25-s plan from Slice B was a happy accident of the still-single-
  output cost formula; landing it deliberately requires per-class cost
  refinement (build/probe weights tuned per fact-vs-dim shape).
- **Q9 canonical 175 rows comes with no row-count gate ratchet.** The
  milestone DoD said `Q9 ≥ 7 (mode-1 baseline)`; updating it to
  `Q9 = 175` is a follow-up housekeeping item once we're confident the
  result is stable across re-runs.
- **`smallAnchorRowsThreshold = 1024`** is the design-default starting
  knob. Q5 fired at the SmallDim path (nation), so the threshold has
  not actually been exercised in production paths yet. If a future
  query produces a Q9-class regression via the anchored rule, tighten
  this value (256 was the suggested fallback in the plan).
- **Plan-snapshot harness false positives in batch mode** (carry from
  M0076-0006). Per-query mode (`--queries=N`) is fully deterministic
  and is the recommended mode for regression locking; batch mode
  remains best-effort.
- **Datum struct packed flip** (carry from M0075-0003). Independent of
  M0077; eligible for M0078 once the arena retention audit (M0076-0002
  carry) is closed.
- **`pprof-data/m0077-final/` captures.** Best-effort follow-up — the
  current focused gate run did not produce profile dumps.

## How to reproduce M0077-final

```sh
git checkout 5d8bd43            # or any HEAD ≥ this commit on try-codex
make bench-build
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

# Focused acceptance gate:
./tpch-runner --queries=3,5,8,9,12,13,21,22 \
    --per-query-timeout=620s --cancel-after=600s

# Plan-snapshot baseline diff:
for q in 1 4 6 14 16 17 19 20 22; do
    ./tmp/plan-snapshot diff --label m0076-baseline-ffc3429 \
        --queries=$q --mode structural
done
# Should print "MATCH" for all.
```

## References

- `docs/handover/2026-05-10-tpch-status-phase8.md` — M0076 close.
- `docs/design/fix-for-q5/{README,01,02,03}.md` — authoritative
  M0077 design bundle.
- `tmp/q5-plan-analysis.md` §2 — Q5 expected post-fix plan shape.
- `tmp/2026-05-10-slice-a-handoff-note.md` — mid-Slice-A takeover
  hand-off (catalog persistence + chained-NLI initial fix).
- `plan_snapshots/m0076-baseline-ffc3429.txt` — regression baseline.
- `plan_snapshots/m0077-final.txt` — Slice D plan capture for the
  full 22-query set.
