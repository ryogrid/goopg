Task: M0142-0008a-3i-plumbing-c9 — LANDED and committed. One real bug found
and fixed (createUniquePath's schema-drift guard was declining on EVERY
call), the next blocker root-caused and filed as c10.

Files this loop:
- internal/optimizer/unnest.go: `existsUnnestSJInfo` gained a
  `srcTableOffset int16` parameter; `SemiRhsExprs[i]` is now a fresh
  `*ColumnRef` with `SourceTableIdx: prm.SubCol.SourceTableIdx +
  srcTableOffset` (matching the sibling `innerKey` field's existing
  expression) instead of assigning `prm.SubCol` verbatim. One production
  call site updated (`unnestExistsExpr`).
- internal/optimizer/exists_unnest_sjinfo_test.go: new assertion in
  `TestExistsUnnestSJInfoSemiHashKey` pinning
  `SemiRhsExprs[0].SourceTableIdx == j.RightKey.SourceTableIdx`. Verified
  FAILS on pre-fix code (temporarily reverted just the `+srcTableOffset`
  term, confirmed `= 1, want 3`, restored the fix).
- internal/optimizer/semiantichain_test.go,
  internal/optimizer/m0142_0008a_3i_plumbing_probe_test.go: updated the two
  `existsUnnestSJInfo(...)` test call sites for the new 4th parameter
  (pass `0`, both call with `params: nil` so the loop never executes).
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §44 —
  method (throwaway env-gated instrumentation, reverted before commit),
  finding 1 (the fixed bug, §44.2), finding 2 (the NOT-fixed c10 blocker,
  §44.3), what c10 needs to do (§44.4), and PG's real Q69 plan shape as the
  eventual target (§44.5).
- docs/design/README.md: m0142-0008a-1 row tail extended (exact-string
  Edit anchored on the c8-row's own trailing sentence — do NOT full-file
  rewrite this row, it is one ~41KB line).
- .ralph/fix_plan.md: `-3i-plumbing-c9` flipped `[x]`; new
  `-3i-plumbing-c10` filed (narrow a semiAnti link's `MinLefthand`/
  `MinRighthand` to the real referenced relation(s) instead of "whole
  atomic outer/inner participant").
- .ralph/deferral_ledger.md: new row, task-id `m0142-0008a-3i-plumbing-c9`
  (covers the c10 handoff).

Key symbols: `existsUnnestSJInfo` (unnest.go:4398, now takes
`srcTableOffset`); `unnestExistsExpr`'s `srcTableOffset` computation
(unnest.go ~4644, unchanged — already correctly sized against
`outerChild.Output()`); `createUniquePath` (createuniquepath.go, unchanged
— its schema-drift guard was CORRECT, the bug was upstream of it);
`jointypeForDirection`'s ANTI arm (joinpaths.go, unchanged — confirmed it
has NO unique-ify fallback, correctly matching PG, which is WHY c10 must
narrow `MinLefthand` rather than add one) — the c10 fix site is
`joinsearchseam.go`'s semiAnti synthetic-leaf loop (~line 665-686).

Findings: (1) `createUniquePath` declined on every real call for Q69's
SEMI leaf: `cr.Name`/`cr.Index` matched `child.Output()` correctly, but
`cr.SourceTableIdx` (1, pre-remap) disagreed with `oc.SourceTableIdx` (5,
post-remap). (2) Root cause: `SemiRhsExprs[i] = prm.SubCol` assigned the
PRE-remap column verbatim, while the sibling `innerKey` field (built from
the SAME `prm.SubCol`, a few lines above) already applies
`+srcTableOffset` for the documented reason ("SubCol was harvested from the
PRE-remap EXISTS body"). Fixed by giving `SemiRhsExprs` the identical
treatment. (3) Post-fix, `createUniquePath` succeeds for Q69's SEMI leaf
for the first time ever (confirmed via instrumentation: `SUCCESS
rel=0x00000008 keyCols=[3]`) — `joinIsLegal`'s unique-ify admission arm now
matches `rel1={customer} rel2={SEMI leaf}` with no error. (4) DP-search
reachability (`jointype=semi`/`anti` DPPATH) is STILL zero for Q69 — the
whole 6-relation search still fails ("failed to build any 4-way joins")
because BOTH of Q69's ANTI links decline `illegal` at every level: their
`MinLefthand` (0x0f / 0x1f) requires the ENTIRE preceding composite, not
just `{customer}`. (5) This traces to `existsUnnestSJInfo`'s own
`minL = clause & synL = synL` line (never narrows past "whole outer side"
once a correlation column exists) — a KNOWN, documented gap ("-3 recomputes
real bits once the RHS actually joins the search") that no loop in this
c-series has actually closed yet; c6/c7/c9 each fixed a DIFFERENT field
(joinInfoList population, qual `.Index` rebase, `SemiRhsExprs`
`SourceTableIdx`) but never `MinLefthand` itself. (6) Confirmed via the
actual SQL (`c.c_customer_sk` is the ONLY correlation column in all 3 of
Q69's semiAnti links) that the true minimal `MinLefthand` is `{customer}`
for all three — the broadening to "whole composite so far" is a
processing-order artifact of `unnestExistsExpr` nesting each new conjunct's
join around the accumulated tree, not a real dependency between the three
independent (ANDed) WHERE-clause conjuncts.

Next step: pick up **M0142-0008a-3i-plumbing-c10** — narrow `MinLefthand`/
`MinRighthand` at SPLICE TIME (joinsearchseam.go's semiAnti synthetic-leaf
loop, ~line 665-686 — NOT at `existsUnnestSJInfo` construction time, before
the outer side's real flat-leaf numbering exists) by resolving each
correlation column (`params[i].OuterRef`/residual columns) against the
outer's real numbering and intersecting, analogous to
`sjiClauseRelids`/`makeSpecialJoinInfoScoped` (specialjoin.go). Must not
regress `TestExistsUnnestSJInfoSemiHashKey`/`AntiHashKey`/`KeylessSemi`
(those correctly pin `existsUnnestSJInfo`'s OWN un-narrowed output; the
narrowing is a later seam-side transformation). Re-run Q69's `EXPLAIN` +
full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep + TPC-DS SF0.25 sweep after
landing.

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` green (includes the fail-then-pass check above,
plus the full targeted SEMI/ANTI/`createUniquePath`/`existsUnnestSJInfo`
test set run individually first). Row-count spot-check against the
git-tracked SF0.25 oracle via direct psql queries against a freshly
restarted sf025 server (private binary `tmp/goopg-m0142-c9-bin`,
built+removed this loop): Q69 = 100 rows (oracle: 100), Q78 = 15 rows /
checksum `c06cf981a7819a37` (oracle: identical) — both unchanged, as
expected. **Could NOT run** `scripts/tpcds-sf025-regression.sh sweep` —
`ci/batch`'s nightly run held the shared SF0.25/SF1 lanes all session (its
own collision guard: "the nightly CI batch is running ... would
contaminate these timings"); the spot-checks above are the substitute
evidence — run the full sweep at the next opportunity.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS except
the same pre-existing `internal/parser` `GroupedJoinUnaliased` AST-drift
failure every recent loop has hit (unrelated, already tracked under
M-NIGHTLY); `internal/optimizer` itself green. `make ralph-state-guard`
auto-repaired the same benign prior-loop clean-exit marker seen every
recent loop, then PASS. Commit's own pre-commit hook runs the pgbench
smoke.

In-flight: none. Private trace binary and sf025 server both stopped/removed
this loop before the final gates ran.
