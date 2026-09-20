# Working Set — loop 2026-09-21 #8

Task: M0145-0005 slice 2 — pulled semi/anti bodies as REAL numbered leaf
items in the jointree problem; retire the synthetic-leaf splice for them.

Files: internal/optimizer/jointreepullup.go (jtPullup.base/nLeaves,
pullUpSublinksIntoJointree appends leafItems to ctx.joinlist,
splicePulledLeaves + shiftRelSetAbove + classifyPulledQuals replace
integratePulledSublinks/buildPulledSemiAntiLink);
internal/optimizer/joinsearchseam.go (nReal/nPulled accounting,
extractSearchLeaves unconditional, remapWalkOrderFlatToSpans gains
pulledBase/pulledLeaves, pgShapedOffsetChecksOK takes nonEmitting RelSet,
pulled-leaf construction arm, searchJl append survives for chain links);
tests: jointreepullup_test.go (RealLeafItems pins), semiantichain_test.go
(admitSemiAnti-false test rewritten to always-admit + call-site updates),
flattened_rhs/in_unnest_sjinfo/m0142 probe call-site updates;
docs/design/0100-0149/m0145-0005 (slice-2 section), docs/design/README.md,
.ralph/fix_plan.md.

Key symbols: pullUpSublinksIntoJointree, splicePulledLeaves,
classifyPulledQuals, shiftRelSetAbove, remapWalkOrderFlatToSpans,
pgShapedOffsetChecksOK, pulledSemiJoinInfo, extractSearchLeaves.

Findings: pulled leaves number into ctx.joinlist at pull-up ([nReal,
nprefix) in binding order); extracted chain leaves keep the tail via a
+nPulled index shift; pulled quals rebase straight into spans space and
feed the plain conjunct pool + joinInfoList — no semiAntiChainLink.
Retired for pulled: synthetic splice, link records, admitSemiAnti flag
(entirely — one call site was literal true since b2), searchJl append.
Chain arm keeps semiAntiChainLink/remap/semiAntiOnQualsOK until slice 3.

Next step: commit slice 2, then per the doc's remaining-slices table —
slice 3 (IR-direct leaf materialisation; retires extractSearchLeaves
node-walk + spans/offset/remap validation family).

Gates run: optimizer suite PASS; units PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); tpcds-sf025 sweep PASS 96/96, plan shapes identical
99/99; tpch-acceptance-arm PASS 24/24 under JOINTREE_PIPELINE=1+PGSHAPED=1
(vs tmp/arm-on-20260920.txt). plan-gate 16/22 vs recorded 14/22 stale-pin
drift — corroborating only: it diffs live-:65433 (old binary), +2 (Q3/Q18)
is the Sep-20 binary rebuild, and neither query can reach the pulled path.

In-flight: none.
