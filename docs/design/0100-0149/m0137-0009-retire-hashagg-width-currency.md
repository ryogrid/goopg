# M0137-0009 — retire `GOOPG_HASHAGG_WIDTH_CURRENCY`, state the arm cap, split the ledger-carry closure

Status: accepted
Date: 2026-09-15
Milestone: M0137 — Parity measurement harness and instrument repair

## Problem

R120 shipped a corrected HashAggregate spill-arm byte currency
(`hashAggTupleWidth`/`hashsize.EntryBytes` keyed on `inNcols`, in place of the
bare `inAvgVarBytes` payload the arm had used since R3) behind a default-off
flag, `GOOPG_HASHAGG_WIDTH_CURRENCY`, on a pre-registered hypothesis: the
corrected currency alone made TPC-DS `aggregation-strategy` worse (69→71) and
lost TPC-H Q10, so R120 REPORT §10 deferred its promote-or-delete decision to
"pair with ncols narrowing" (R124) rather than resolve it standalone.

R124 ran that pairing on TPC-DS SF0.25 (both flags on) and measured it
**identical** to R120's arm alone (GroupAgg 29, HashAgg 123,
`aggregation-strategy` 71 either way). R124 §7 states the conclusion
explicitly: *"R120's flag does not become correct once ncols is narrowed. Its
promote-or-delete decision can now be resolved on evidence — **delete**,
rather than carried to yet another round, since the condition it was waiting
for has arrived and changed nothing."*

That deletion had not happened by the time M0137 was filed
(`docs/milestones/0137-parity-measurement-harness-and-instrument-repair.md`
names it directly in its Definition of Done), and `02-open-problems.md` N16 /
`01-what-we-learned.md` F14 both flag it as still outstanding. R121 §6
separately named a norm that was never landed anywhere binding: *"default-off
arms are evidence, but past about four they become debt… that belongs in the
take2 charter rather than buried in a round REPORT, and is flagged here for
someone to move."* Nobody moved it (`03-process-retrospective.md` P7).

`03-process-retrospective.md` P7 also names a third, larger pathology under
the same "Debt accumulates with no retirement mechanism" heading — **ledger
carry**: *"the same items appear verbatim across ten-plus rounds: R51 items
2–3, R52 §4.2, R54 follow-ups, '#6', R61-#4, the NLI staleness comment, R55 §3
tie-break, F3 procost, the Q8 gap, AGG_MIXED."* The fix_plan's M0137-0009 line
bundles all three (flag deletion, arm-cap statement, ledger-carry closure)
into one task.

## What landed

**1. `GOOPG_HASHAGG_WIDTH_CURRENCY` is deleted**, not merely left permanently
off:

- `internal/optimizer/hashagg_widthcurrency.go` and its test file are
  removed. `hashAggWidthCurrencyEnabled`/`hashAggWidthCurrencyFromEnv`/
  `hashAggTupleWidth`/`setHashAggWidthCurrencyForTest` no longer exist.
- `cost_funcs.go`'s spill-arm block (`costAgg`, `AggStrategyHashed` tail) is
  now permanently the flag's former OFF arm: `entry := hashAggEntrySize(nAggs,
  inAvgVarBytes)` and `pages := tuples * inAvgVarBytes / blockSizeBytes`, with
  no `widthCurrency`/`armLive`/`entryWidth`/`rowBytes` branching. The doc
  comments at the function head and at the arm itself now narrate the R120→
  R124→M0137-0009 history and explicitly name the KNOWN residual PG
  divergence this leaves (the arm still prices in the variable-payload-only
  currency, under-stating the true tuple width — closing that is the
  K65/K66 ncols-narrowing family, deliberately not restored here).
- `inNcols` stays a `costAgg` parameter (many call sites already compute and
  pass it) but is unused within the function body as of this change; the doc
  comment says so explicitly rather than leaving the previous "LIVE inputs:
  they decide whether the arm fires" claim silently wrong the way the R3→R120
  transition once did (`cost_funcs.go`'s own comment calls out that earlier
  staleness).
- `internal/optimizer/flaglabels.go`: the resolver entry for
  `GOOPG_HASHAGG_WIDTH_CURRENCY` in `flagResolvedState` is removed; the name
  moves from being resolvable to `flagProvenanceRetired["GOOPG_HASHAGG_WIDTH_CURRENCY"]
  = "M0137-0009"`, following the exact pattern already established for
  `GOOPG_RELSIZE_FALLBACK` / `GOOPG_COST_DRIVEN_JOINORDER` / `GOOPG_MHJ_PACKING_OFF`
  / `GOOPG_PGSHAPED_COLLAPSE` / `GOOPG_GS_SHARE_SOURCE` (`flaglabels.go`'s own
  comment: the row survives retirement so an older artefact that DOES carry
  the variable is still attributable to a real gate version). `scripts/planner-flags.env`
  regenerated via `go run ./cmd/gen-planner-flag-labels`; the only diff is the
  retired row's label (`unset(off)` → `retired(M0137-0009)`) plus the
  generated `GOOPG_PLANNER_FLAG_RETIRED_GOOPG_HASHAGG_WIDTH_CURRENCY=1` line.

**2. The default-off cost-arm cap is now stated in the harness.**
`AGENT.md` §"Plan-parity harness"'s "Known-stale claims" bullet is rewritten:
at HEAD there are **two** default-off cost arms
(`GOOPG_PG_HASH_TUPLE_SPILL_COST` R108, `GOOPG_PG_SORT_RELATION_BYTES_COST`
R113), not three or four, and the bullet now states R121 §6's norm verbatim
as a binding rule rather than a stale count correction: a default-off cost arm
is evidence, not a standing feature, and past about four accumulating at once
they become debt — each one needs an explicit expiry (a measurement or a
landed consumer) that resolves it to promote or delete. This is the "someone"
R121 asked for; the norm's home is now the harness section itself, the exact
place `03-process-retrospective.md` P7 said it belonged.

**3. Ledger-carry closure (P7's ten-plus-round list) is SPLIT OUT, not done
here.** Filed as `M0137-0013` in `.ralph/fix_plan.md`, modeled on
M0137-0012's already-established pattern (file/verify, no speculative code
change). Reasoning for the split, not a silent drop:

- The list P7 names — *R51 items 2–3, R52 §4.2, R54 follow-ups, "#6", R61-#4,
  the NLI staleness comment, R55 §3 tie-break, F3 procost, the Q8 gap,
  AGG_MIXED* — is ten independent threads, each requiring re-reading a chain
  of `TODO.md` round entries (R51 through R81+ in places) to determine
  whether a later round already discharged it, changed its grounds, or it is
  still genuinely open, before it can be "closed or deleted" honestly. That
  is qualitatively different work from the flag deletion above (one
  mechanically-scoped deletion with an already-adjudicated verdict cited by
  name, R124 §7) — it is a determination-per-item campaign.
- The harness itself draws exactly this distinction for the same kind of
  bundling mistake: R121 REPORT's own words, quoted in `AGENT.md` "Way of
  working" §, warn against folding a *"candidate cost model held off pending
  evidence"* (R108/R113/R120 — this task's flag) together with *"enabling
  infrastructure with no consumer yet"* (R121) into one bucket, because it
  *"flattens that distinction."* P7's ledger-carry items are a third,
  different kind of thing again (unresolved-status *questions*, not
  code-behind-a-flag), and forcing all three kinds through one task in one
  loop would produce the same shallow, over-broad round R127 was withdrawn
  for (`AGENT.md`'s "What to read" section) — a determination made without
  having actually reopened and reconciled the cited rounds.
- `.ralph/PROMPT.md`'s deferral discipline requires this split to be visible,
  not implicit: M0137-0013 is filed in `.ralph/fix_plan.md` in the same
  commit as this doc, citing the exact P7 list, so the obligation is not
  lost the way R80's silent skip or R81's ownerless Q4 close-out were
  (`03-process-retrospective.md` P7, same paragraph).

## What did not change

No plan-shaping behavior change on any query: `GOOPG_HASHAGG_WIDTH_CURRENCY`
shipped default-off, so deleting it removes only dead-when-unset code and its
provenance-table entry — the arm's arithmetic at HEAD is bit-identical to
HEAD before this change (verified: `go test ./internal/optimizer/...` passes,
including `TestCostAggHashSpillInertBelowThreshold`,
`TestCostAggHashSpillChargedAboveThreshold`, and
`TestHashAggEntrySizeAndLimits`, all of which exercise the surviving legacy
arithmetic directly and were unaffected by this change). No round directory
under `plan_parity_fix_take2/` was edited (frozen history). No ledger row:
this is instrument/debt-retirement bookkeeping over an already-adjudicated
decision (R124 §7), not a newly discovered PG-incompatibility — the KNOWN
residual divergence the deleted arm never closed (goopg's spill-arm currency
still under-states true tuple width) is not new; it is the pre-existing gap
`cost_funcs.go`'s doc comment already named and still names, now pointing at
the K65/K66 ncols-narrowing family as its resolution path.

## Verification

- `go build ./internal/optimizer/...` clean.
- `go test ./internal/optimizer/...` — full package suite passes, including
  the flag-provenance guards (`TestFlagProvenanceEnvIsGenerated`,
  `TestFlagLabelsRoundTrip`, `TestFlagProvenanceTableIsResolvable`,
  `TestFlagProvenanceTableCoversPlannerEnv`,
  `TestFlagProvenanceDetectorSeesHelperWrappedReads`,
  `TestGateScriptsUseGeneratedFlagLabels`) and the surviving HashAggregate
  spill-arm pins in `cost_funcs_test.go`.
- `scripts/planner-flags.env` regenerated and diffed by hand: the only change
  is the one retired row, matching the established retirement shape used by
  the five prior retired flags in `flaglabels.go`.
- No `make plan-gate` / TPC-H / TPC-DS sweep run: the change is a deletion of
  an already-default-off arm with no reachable behavior change, so a plan/
  value sweep would measure noise, not this change — consistent with the
  harness's own "recon task… no production change" framing not applying here
  (this IS a production change, but a provably inert one) and with M0137-0005's
  established baseline (`make plan-gate` compares goopg against committed
  goopg, not live PG) not being the right instrument for a bit-identical
  arithmetic claim that unit tests already pin directly.
