(idle — nothing in flight)

Last loop: **M0127-P5.6 roll-up CLOSED** — every sub-item was already `[x]`; what
the roll-up still owed was the residue in its own body and the acceptance re-read.

**The residue was "re-evaluate M0125-0003 stage 3 against 04 §2's rows-once
discipline", and the answer is that stage 3 is not a staged flag consumer and
never will be.** Stage 3 was filed as "make `estimateBaseRelInfo.baseRows`
positive cold"; under the OLD DP that would have SHADOWED the stage-2 seed tier,
and sequencing that shadowing is the entire reason the flag was staged. Rows-once
deletes the second consumer — the search reads a base rel's cardinality exactly
once, `initialRelRows` → `baseRelInfo.filteredRows` — so the placement is simply
correct at the stage the seam already runs at. `GOOPG_RELSIZE_FALLBACK=3` stays a
documented alias for stage-2 behaviour.

Landed as `applyRelSizeFallback` (relsize.go) replacing joinsearchseam.go's inline
tier, in upstream's order: `estimate_rel_size` (plancat.c:1075) supplies pre-filter
`tuples`, `set_baserel_size_estimates` (costsize.c:5378) multiplies by
`clauselist_selectivity` — here `applyLocalFilterSelectivity`, factored out of
`estimateBaseRelInfo` so the twins cannot drift (rule #2).

**Why it is a re-derivation, and the one place it is NOT:** the fallback fires
only when `tableRows` answered 0, and for `Stats == nil` that is also the state
where `columnStatsForChild` answers nil for every column → every clause
`reliable=false` → unscaled → identical to the pre-filter stamping. **But
`Analyzed=true, Columns populated, RowCount=0` is a REAL state**, not an edge
case: it is what `loadStatisticsFromHeap` leaves behind (column stats survive a
restart, `RowCount` does not — ledger pq-P6). There the old stamping threw the
restored MCV list away; the new placement spends it, as PG does.

Acceptance re-read (not re-run) from the default-arm audit
`analysis/leftdeep-joins/2026-08-05-p59run4-audit-off.txt`: **Q9 final joinrel
6.3×** (est=1999060 actual=316264) vs the ≤10² bar, **parity_violations=0**. The
lone absolute tripwire, Q18 25 526×, is what 09 §4.1's parity ratchet exists for
(PG 18.3 is at 5 386×/9 428× on its own shapes there).

Files: `internal/planner/{cardinality.go,relsize.go,joinsearchseam.go}`,
`relsize_baserel_placement_test.go` (new, 4 tests). Docs: 04 §2 + new §2.1,
IMPLEMENTATION-TODO P5.6 tick, design README index, M0125-0003's re-scope note
amended with the verdict, 1 ledger row.

Gates run: UNITS 0 FAIL; **SPOT PASS (Q12=2, Q13=35)** — and SPOT is S-cold, i.e.
it runs the exact state the change is proven inert in. DS05 not re-run,
structurally: the changed arm needs `RowCount == 0` with populated column stats,
and the DS05 cluster persists both (M0125-0028/-0029), so no gate query reaches it.

NEXT LOOP (banner wins — M0127 is #3 and current). Topmost unchecked M0127 item is
now the roll-up **P5.7**, whose sub-items -a and -b are both `[x]`: same question
as P5.6 — is it a tick or does its body carry residue? Its stated bar is "UNITS +
PLAN (default arm ZERO diffs)", and both sub-items argued PLAN was structurally
inapplicable because `hashJoinCost`'s callers sat behind OFF-by-default gates —
**re-check that claim, because the searched planner is demonstrably live in the
default arm now** (last loop's DS05 moved Q6's shape via a searched Memoize path).
Then **PS6.1/PS6.2** and the P6.x deletion series.

Nightly triage: `ci/logs/action-items.md` still run 20260806-011323, 18 items, all
subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
