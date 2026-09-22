# Working set — 2026-09-23 (loop after M0145-0005 slice 6)

`Task:` M0145-0005 — single-pass DP over the jointree. **Slice 6 landed
and committed as `171c110e7`** ("chain-extracted semi/anti as deferred
real leaf items"): under `jointreePipeline` every SEMI/ANTI link's RHS
is a real joinlist leaf item in a deferred band `[emitTotal,
emitTotal+nSemiAnti)` — no more synthetic tail for chain semianti;
`semiAntiChainLink.realLeaf` marks them; new `chain-leaf-desync`
fail-closed check. Knob-arm SF0.25 seam census: `semianti-not-tail` gone
entirely (Q78's class). All 4 gate stamps PASS on the committed index +
pre-commit pgbench smoke.

`Files:` internal/optimizer/{collapse,specialjoin,joinsearchseam,jointreepullup}.go,
{joinsearch_m0145,jointreescope,jointreepullup}_test.go,
docs/design/0100-0149/m0145-0005-*.md (new "Slice 6" section),
.ralph/fix_plan.md (nested bullet).

`Key symbols:` `jointreeItemEmittingRels`, `sjiLeaf.leafIdx`,
`makeSpecialJoinInfoForSets`, `extractScopeLeaves(tab, sjis)`,
`splicePulledLeaves(pu, nReal, nprefix, ...)`, `semiAntiChainLink.realLeaf`,
`pulledBase` in the seam's non-emitting construction band.

`Hypothesis/Findings:` problem layout is now emitting [0,emitTotal) |
deferred-chain [emitTotal,emitTotal+nChain) | pulled tail; nChain =
nprefix - nPulled - nrels (nrels == emitting bindings count). The
`searchJl` leafItem-append now appends zero items on the jointree arm.
Legacy arm unchanged (antiCollapsedJoins path intact).

`Next step:` per the fix_plan banner — M0145-0005 remains `[ ]` with
slice 7 ledgered: Phase A/B split retirement (`runJoinSearchBelowPinned`,
pinned spine, splice-time re-resolution). Then banner order continues
(0007, 0008, 0010, M0145-0018 owner-GO firewall relaxation).

`Gates run:` optimizer suite PASS; RALPH_PRECOMMIT_SCOPE=units PASS;
tpch-spotcheck PASS (Q12=2, Q13=33); tpcds-sf025 PASS=96 MISMATCH=0
TIMEOUT=0 plans 99/99; tpch-acceptance-arm PGSHAPED=1 24 MATCH PASS
(vs tmp/m0122-0015-arm.txt); tpcds-fireset PASS (sf025+sf1,
introduced=none); pre-commit pgbench smoke PASS.

`In-flight:` none — all gates consumed, commit pushed pending (see git
status; push is the last step of this loop).
