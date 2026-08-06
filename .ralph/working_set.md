(idle — nothing in flight)

Last loop: **M0127-P5.4b-ii-b-2 CLOSED** — Memoize is now a searched PATH.

The item was DEPENDENCY-DEFERRED on 2026-08-04 pending P5.5's `createPlan`
arms; P5.5 has landed, so it was re-selected as the topmost unchecked M0127
item, exactly as its own note instructed.

**Memoize is a PATH, not an attachment, and that was forced.** goopg already had
`maybeAttachMemoize` (`memoize.go`) running on a BUILT `*NestedLoopIndexJoin` —
but `walkRewriteNLI` skips a searched subtree (nl_index_join.go:110), so no
searched NLI ever reached it. Attaching at `createPlan` time would have made the
executed plan cheaper than the plan the search costed, when the point of
memoizing is that an NLI beats a hash join it would otherwise LOSE to; that
comparison happens in `addPath` or nowhere. So: new `PathMemoize` kind +
`MemoizeInfo` carrier, `getMemoizePath` (= `get_memoize_path`, joinpath.c:674),
`costMemoizeRescan` (= costsize.c:2541), and `addNLIPaths` offering the bare AND
the wrapped inner to `addPath` — `match_unsorted_outer`'s own shape (:1965-1986).
`PathMemoize` has NO `createPlan` arm (it panics there): goopg's cache is
`NestedLoopIndexJoin.InnerMemo`, a field on the join, so the NLI arm unwraps it
and keys the cache on the ALREADY-TRANSLATED probe keys — one list read twice,
since `memoizeOp` and `indexScanOp.Rescan` read the same bound outer slot.

**The §5.2 binding contract was discharged in a different form than filed:**
`tryBuildNLI` is not the searched constructor at all; `createNestLoopIndexJoinPlan`
is, its declines are panics, and eligibility is shared at the PRODUCER
(`pickIndexCoveringAllLeadingColumns`, `addParameterizedIndexPaths`). Two
constructors now exist on disjoint trees — ledgered against the S7 deletion of
the legacy one.

Files: `internal/planner/joinpathsmemoize.go` + `_test.go` (new),
`path.go`, `joinpaths.go` (`searchCtx` threaded), `joinpathsnli.go`,
`joinrelsize.go`, `createplan.go`, `createplannl.go`; 6 test files took a `nil`
searchCtx arg. Docs: 03 §5.2 status + IMPLEMENTATION-TODO tick; 3 ledger rows.

Gates run: UNITS 0 FAIL; SPOT PASS (Q12=2, Q13=35); **DS05 PASS=95 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, runtime-moves=0**, plan channel: ONE shape
change — **Q6's `date_dim_pkey` probe now wrapped in `Memoize (Cache Key:
s.ss_sold_date_sk)`**, same rows, same checksum. That is the end-to-end proof.
Note for future loops: the "no stats → no plan can move" argument covers SPOT
only. DS05 persists per-column stats since M0125-0028/-0029 and IS load-bearing.

NEXT LOOP (banner wins — M0127 is #3 and current). Topmost unchecked M0127 items
are the roll-ups **P5.6** and **P5.7**, whose every sub-item is `[x]`: check
whether they are ticks or carry residue (P5.6's body still names "re-evaluate
M0125-0003 stage 3, rows-once per RelOptInfo, 04 §2"). Then **PS6.1/PS6.2**
(compiled HashKeys accessors + sibling audit) and the P6.x deletion series.

Nightly triage: `ci/logs/action-items.md` still run 20260806-011323, 18 items,
all subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
