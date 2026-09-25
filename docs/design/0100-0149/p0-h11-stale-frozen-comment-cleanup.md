# P0-H11 — stale-comment cleanup after the owner's M0142-0008 unfreeze decision

Status: implemented (Loop #32). Comment/doc-text-only change — zero
behavior delta. PG citation n/a (no PG semantics touched; C2 N/A).

## 1. Task and scope

`.ralph/fix_plan.md` `P0-H11` (Kind: impl, Parent: P0-E7, filed under banner
item 1). The owner's 2026-09-20 decision UNFROZE the M0142-0008 chain — so
P0-H11's "remove `admitSemiAnti stays false` comments if the chain is
removed" branch does not fire. The live branch is instead: **audit stale
FROZEN/deferred phrasing in code comments and docs against the new unfrozen
state**, plus the still-valid sub-item: **bring the `Status:` lines of the
design docs for M0142-0003d/e/f/g/j/k up to date** (none were present).

## 2. What is actually stale at HEAD

Two independent facts moved since the comments were written:

1. `admitSemiAnti` was flipped to a literal `true` at the one production
   `extractSearchLeaves` call site by `M0142-0008a-3i-plumbing-b2` step (iii)
   (`joinsearchseam.go:313`, `extractSearchLeaves(chain, true)`), well before
   the unfreeze — and comments written between b1 and c17 still assert
   "stays `false` in production".
2. The owner lifted the S5b/chain freeze on 2026-09-20 (banner UNFROZEN
   block; deferral-ledger row `csq-R2` updated by owner) — comments that
   present "S5b deferred by user decision" or "the chain is frozen" as the
   current state are stale.
3. `M0142-0008-producer` (Loop #31, `1f76d83d9`) made `unnestInExpr` /
   `unnestNonCorrelatedInExpr` set `Join.SJInfo` — stale claims that the
   IN/NOT-IN unnest paths "never set `.SJInfo`" are now wrong (only the
   `JoinTypeInner` `unnestScalarWithResiduals` path correctly never carries
   one).

### 2.1 Inventory of corrected sites

Production code (`internal/optimizer/`):

- `predp.go` file header (~:13-15): "DP participation for semi/anti (S5b) is
  deferred by user decision (2026-07-21)" → the deferral was lifted
  2026-09-20; the pinned-spine structure it describes is still accurate, so
  the sentence is rewritten to record the lift and the current reachability
  blocker (leaf-count gate) instead of deleting the context.
- `predp.go` `spliceSearchedSpine` header (~:266): "unreachable from any live
  fixture while `admitSemiAnti` stays `false`" → `admitSemiAnti` is `true`;
  the success path stays unexercised by the corpus today for a different
  reason (every IN/EXISTS chain still declines at `leaf-count`,
  `joinsearchseam.go:325`).
- `joinsearchseam.go` `extractSearchLeaves` header (~:1269): "the ONE
  production call site … can pass a literal `false` … provably inert in
  production until b2 lands" → b2 landed; the parameter still exists so the
  false-arm stays unit-testable, but the production claim is corrected.
- `joinsearchseam.go` semiAnti arm (~:1319): "INERT until admitSemiAnti is
  true" → it is true. The aside "still out-of-scope S5b mechanism" is
  corrected to "the remaining S5b increments" — the chain is unfrozen, not
  out of scope.
- `joinsearchseam.go` c17 block (~:617): "the IN/NOT-IN unnesting paths …
  never set `.SJInfo`" → corrected: the IN paths now set it
  (M0142-0008-producer); only `JoinTypeInner` paths legitimately carry none.
- `joinsearchseam.go` `buildLeafSpans` header (~:1552): "today's only
  production shape — admitSemiAnti stays false" → corrected.
- `joinsearchseam.go` `pgShapedOffsetChecksOK` header (~:1654): "always
  passes `admitSemiAnti=false` … fully inert in production … until b2 flips"
  → corrected.
- `joinsearchseam.go` `cumulativeFromSpans` header (~:1687): "true today
  because this function's only caller is reached with admitSemiAnti=false"
  → corrected, and the latent question it was hiding is filed (§3).

Test comments (same staleness, no behavior change):

- `predp_test.go` (~:277): same "stays `false` in production" claim →
  corrected.
- `semiantichain_test.go` (~:414): "the moment admitSemiAnti is ever wired
  live" → it is wired live.
- `semiantichain_test.go` (~:533): "(the literal value the ONE production
  call site passes …)" → the call site passes `true`; the test deliberately
  exercises the `false` arm's contract.
- `semiantichain_test.go` (~:699): "today's only production shape, since
  … always passes `admitSemiAnti=false`" → corrected.

Docs:

- `m0142-0008a-1-semi-anti-sji-design.md` `Status:` line claimed
  "design-only, no production diff" — stale now that the doc records the
  landed a-3 plumbing increments (b1/b2, c1-c17) and producer; updated to
  describe the doc as the chain's living design record.

### 2.2 `Status:` lines added to the six 0003* docs

None of `m0142-0003d/e/f/g/j/k` carried a `Status:` line at all ("none
present" in the task text was literal). Added, matching each fix_plan
entry's recorded outcome:

- `0003d`: closed (recon — premise refuted)
- `0003e`: closed (recon — bench schema missing FKs identified)
- `0003f`: implemented (partial: 11/16 FKs at close; remaining 5 landed under
  M0142-0003i)
- `0003g`: implemented (FK validation index-accelerated)
- `0003j`: closed (recon — evidence chain complete; resolution carried by
  P0-E4/E5/E6)
- `0003k`: closed (recon — hypothesis A refuted; (c)/(d) resolved by
  P0-E4/E5/E6)

README status column synced to match.

## 3. Finding surfaced by the audit (filed, not fixed here)

`cumulativeFromSpans`'s stale comment asserted a load-bearing invariant —
"valid only when `spans` is contiguous and monotonic … true today because
the only caller is reached with `admitSemiAnti=false`". The caller
(`joinsearchseam.go:750`) is fed by `buildLeafSpans(widths, semiAnti)`, which
by design appends synthetic Semi/Anti RHS ranges **out-of-band** (after the
real-width total). Round-tripping such spans through `[]int` and back via
`spansFromCumulative` (joinrestrict.go:482, consumer relfromjoinlist.go:690)
would misattribute the hole between the last real leaf and the first
synthetic span to the last real leaf. **Not reachable in production today**:
every corpus chain that would carry a semiAnti link declines earlier at the
`leaf-count` gate (M0142-0008-producer loop's measurement), so no synthetic
span can reach `cumulativeFromSpans` yet. This is a latent correctness item
for the pending leaf-admission work — recorded in
`.ralph/fix_plan.md` under `M0142-0008a-3` and in
`m0142-0008-producer-in-unnest-sjinfo.md` §6's resume notes, NOT silently
fixed in a comment-cleanup task.

## 4. Gates

Comment/doc-only change. Hook rules (`arm_needed=1` for
`internal/optimizer/*`) still require the full stamp set on the staged tree:

- units (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`)
- `scripts/tpch-spotcheck.sh`
- `scripts/tpcds-sf025-regression.sh sweep`
- `scripts/tpch-acceptance-arm.sh` (PGSHAPED=1, seed 20260905, PER_Q=900,
  vs the post-reload baseline re-captured in `f8b80ec80`)

Results recorded in the commit body / fix_plan entry.
