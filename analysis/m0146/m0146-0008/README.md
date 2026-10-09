# M0146-0008 — leaf-count residual re-census + admission (closes by measurement)

Re-census of the `leaf-count` seam-decline population on the post-flip
(jointree-only) default arm, per the task's mandate: re-census the
opaque-leaf population M0144-0003a measured, then implement whatever
leaf/admission work is still real.

## Method

HEAD `81abd893a` (2026-09-28). Private offline `cp -a` clone of
`bench/tpcds/runtime_goopg/data-sf025` (the gate cluster was down, so the
copy is a clean quiescent snapshot) on port 5591, built at HEAD with a
temporary LCFIT probe at the `leaf-count` decline site
(joinsearchseam.go:414) printing each scan leaf's Go type, join type,
`nprefix` and `nSynthetic`. Server ran under the cgroup cap with
`GOOPG_PGSHAPED_DP_TRACE=1`; all 99 SF0.25 TPC-DS query files were EXPLAINed
statement-by-statement (the sweep's exact wrapping) with per-query log-delta
attribution. The probe was reverted after the capture; nothing under
`internal/` is committed.

## Result

`declines-sf025.txt`: **2 `leaf-count` declines in the whole corpus**
(Q51, Q97), `nrels=2 nleaves=1` each.

Baseline for comparison — M0144-0003a's pre-flip census
(`analysis/m0144/m0144-0003a-opaque-leaf-census.txt` /
`-project-descent-refuted.txt`): **26 declines across 11 queries**
(Q14 Q16 Q23 Q33 Q35 Q56 Q58 Q60 Q83 Q94 Q95), every one an opaque leaf
wrapping an already-planned composite (`*Project` over
`*Gather`/`*CTEScan`/`*Filter`/`*Join`/`*NestedLoopIndexJoin`).

**That population is gone: 26 → 0.** The jointree arm dissolves it because
sublink bodies are pulled up into the jointree IR before planning
(M0145-0003), so leaf slots hold real FROM items instead of finished plan
fragments.

## The residual is a different wall, not leaf admission

`lcfit-probe.txt`: both surviving declines carry
`scan[0] = *optimizer.Join jtype=3` = `JoinTypeFull` — the whole
two-relation chain is one `FULL OUTER JOIN`:

- Q51: `web_v1 web FULL OUTER JOIN store_v1 store` (CTE-body scope
  `web_sales,date_dim` / `store_sales,date_dim` — same FULL top).
- Q97: `ssci FULL OUTER JOIN csci`.

The seam walk appends a FULL `*Join` as one opaque leaf
(joinsearchseam.go:1808-1813 — any join type outside
Cross/Inner/Left/Right/Semi/Anti), so `scans=1` vs `nprefix=2` trips
`leaf-count`. This is the documented fail-closed FULL pin: `joinPinned`
pins FULL (and outer-over-FULL), and the file-header comment names
leaf-count as its designed decline site. It is not an opaque-leaf /
admission problem — there is no swallowed subtree to decompose, and at
nprefix=2 the DP search has no ordering freedom anyway; the syntactic
fall-back builds the only possible join tree and join-method selection
still runs on it.

## Verdict

Closes by measurement. The admission work the task was filed to find does
not exist on the jointree arm; the residual declines belong to the
already-documented FULL-join pin class.
