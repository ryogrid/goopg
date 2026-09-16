Task: M0142-0008a-3i-plumbing-c18 (recon, DONE) — full-corpus SF0.25 recheck
of Semi/Anti DPPATH reachability after c1-c17 landed. Also closed out c11's
stale checkbox (its (a)/(b)/(c) were all actually resolved by c12/c16).

Files: .ralph/fix_plan.md (c11 -> [x] with closing note; new c18 entry),
.ralph/deferral_ledger.md (c18 row), docs/design/0100-0149/
m0142-0008a-1-semi-anti-sji-design.md (§54), docs/design/README.md
(m0142-0008a-1 row: added missing c17 summary + new c18 summary — c17 had
landed its design-doc section but never updated this index row).
No internal/ source changed this loop (recon only).

Key symbols: extractSearchLeaves (joinsearchseam.go:1248, admission arm at
1287), semiAntiLinksHaveSJInfos (the decline at line ~591), semiAntiChainLink
.sjinfo = j.SJInfo (line ~1360), reduce_outer_joins.go's demotedForPlan
(line 102, Q78's ANTI-join producer), existsUnnestSJInfo (unnest.go:4837,
the ONLY current .SJInfo setter), deconstructJointreeScopedSJI
(planner.go:3051), whereEligibleForPreDPUnnest (predp.go:35).

Findings: full 96-query SF0.25 sweep (private GOOPG_BIN, sf025 gate green:
PASS=96 MISMATCH=0 ERROR=0) + GOOPG_PGSHAPED_DP_TRACE=1 + per-query
truncated-log attribution (run each query*.sql alone, grep the trace
between runs — the sweep log itself has no per-query markers) shows STILL
0 jointype=semi/anti DPPATH lines even with the whole c1-c17 chain landed.
Exactly one query (Q78) reaches the semiAnti admission arm at all (3
declines, reason=semianti-link-no-sjinfo, matching its 3 CTEs) — via
reduce_outer_joins.go's LEFT-JOIN-to-ANTI-JOIN strength reduction, NOT
EXISTS/IN unnesting, so existsUnnestSJInfo never touches it and .SJInfo
stays nil on that *Join node. Q10/Q16/Q35/Q69/Q94 (the family every prior
c9-c17 note assumed was "most likely") show ZERO semiAnti trace of any
kind — their Semi/Anti joins apparently never reach the search's input
tree at all (separate, unfiled, larger question — not this loop's scope).

Next step: pick ONE of design doc §54's two candidate fixes for Q78 —
(b) is recommended first (smaller, more PG-faithful): in the semiAnti link
constructor (joinsearchseam.go:1360), when j.SJInfo == nil, fall back to a
relids-keyed lookup in ctx.joinInfoList instead of declining outright.
Verify via this loop's exact method (per-query truncated trace on Q78
alone: 3 declines should become 3 accepts with real jointype=anti DPPATH
lines), then a full SF0.25 sweep to confirm no plan-shape regression
anywhere (PASS=96 must hold, Q78's own result must stay byte-identical to
oracle). Separately — NOT blocking, can be picked up independently or
later — the bigger Q10/16/35/69/94 non-reachability question from design
doc §54 remains completely untraced.

Gates run: make ralph-state-guard (self-repaired a stale
running/completed mismatch, passed). scripts/tpcds-sf025-regression.sh
sweep x2 this loop (both PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0 SKIP=3, private GOOPG_BIN, no production diff). tpch-spotcheck
not run (no code change, N/A; also still blocked on M0142-0003k's data
reload per CLAUDE.md).

In-flight: none. Private trace server stopped, tmp/goopg-sf025-trace-bin
and tmp/m0142-c11-recheck-sf025/ removed after the recon.
