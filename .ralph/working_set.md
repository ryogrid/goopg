Task: M0141-S2a-fix scoping recon (banner's TOP-PRIORITY pairing, other half
of "M0141-S2a-fix and M0139-0007"; M0139-0007 was closed last loop). **DONE
this loop, recon-only, NO production diff** (verified: `git diff --stat --
'*.go'` empty; committed 86af308).

Files: `docs/design/0100-0149/m0141-s2a-fix-scoping-recon.md` (new),
`docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0141-S2a-fix
annotated with the scoping findings; filed M0141-S2a-fix1 and
M0141-S2a-fix2 as the two concrete next slices), `.ralph/deferral_ledger.md`
(+1 row, task-id `m0141-s2a-fix-scoping`).

What was found: M0141-S2a-fix's own text framed half (1) ("move narrowing
earlier or compute a preview at cost time") as possible
planner-search-order surgery. Tracing the call graph shows it is NOT:
`agg.InputTarget []int` (a NAME-derived keep-list) is already stamped by
`stampAggregateInputTarget` inside `buildAggregateStage` (`planner.go:8219`)
— strictly BEFORE `createGroupingPaths`'s Hashed-vs-Sorted cost contest runs
(`planner.go:1760`) — and `addGroupingPaths` (which calls `aggInputWidth`,
`groupingpaths.go:344`) already receives `aggNode` as a parameter. The
"preview" half (1) asked for already exists, unused, right at the call
site. B2 derivation from `./postgres/`: `cost_agg`'s caller
(`pathnode.c:3430`) passes `subpath->pathtarget->width`; the executor's
`hash_agg_entry_size` (`nodeAgg.c:1701`, called at `:3701-3703`) uses
`outerplan->plan_width` — both PG sites key on the SAME quantity, the
query-wide up-front narrow target-list PG's planner builds once
(`build_joinrel_tlist` etc.), which `agg.InputTarget` is goopg's own
per-node/later equivalent of. Sizing verdict: attempt the width-preview
wiring ALONE first (cheap, 3-line, composes across all 3 `aggInputWidth`
call sites); gate the `hashAggEntrySize` fixed-overhead currency correction
on that result — R124 §7's prior "currency + narrowing" test never had a
live-at-cost-time preview to pair with, so it isn't dispositive against
retrying now. Also recorded (design doc's "Correctness note"): feeding a
narrowed preview to the HASHED candidate is safe under B2 even though
goopg's real narrowing commit (`narrowAggregateInput`) later DECLINES for
Hashed strategy (no `*Sort` to sink a Project below, per its `pastSort`
retention-site condition) — PG's own entry-size formula charges width
regardless of row retention, matching B2's "goopg may still really run at
its old footprint; that is not a regression" worked example.

Key symbols: `internal/optimizer/group_input_target.go`
(`stampAggregateInputTarget:267`, `deriveAggregateInputKeep:190`),
`internal/optimizer/planner.go` (`buildAggregateStage` call `:1738` ->
internal stamp `:8219`; `createGroupingPaths` call `:1760`),
`internal/optimizer/groupingpaths.go` (`aggInputWidth:326`,
`addGroupingPaths:340` — the M0141-S2a-fix1 target, plus its two siblings
`partialaggpaths.go:338`, `partialaggupper.go:327`),
`internal/optimizer/cost_funcs.go` (`costAgg:431`, HASHED spill arm
`:516-540` — the M0141-S2a-fix2 target, `hashAggEntrySize`),
`internal/optimizer/upper_narrow_apply.go` (`narrowAggregateInput:210`,
`pastSort` retention-site condition `:267` — cited for the correctness
note, not touched).

Gates run: no `.go` files touched this loop (confirmed via `git diff
--stat -- '*.go'`), so no build/test gate was needed for the recon itself.
`make ralph-state-guard`: found a stale status/progress mismatch from a
prior loop's clean-exit marker (status="running"/progress="completed"),
self-repaired to consistent ("running"/"in_progress"), exit clean on
second run. Nightly triage checked: `ci/logs/action-items.md`'s
20260914-235643 run (14 items) was already fully filed under M-NIGHTLY as
of the "filed 2026-09-15" section — no new filing needed this loop.

In-flight: none.

Next step: re-read the `## Current Priority` banner fresh. The banner's
pairing "M0141-S2a-fix and M0139-0007" is now BOTH scoped (M0139-0007 last
loop, M0141-S2a-fix this loop) — re-check whether the banner has been
rewritten to reflect this before picking. If unchanged, the natural next
pick is **M0141-S2a-fix1** (the width-preview wiring: restrict
`aggInputWidth`'s three call sites to `agg.InputTarget`'s kept columns when
`InputTargetKnown`; pin with a test per B2's "pinned by a test" rule;
re-measure Q3/Q13/Q18 + the TPC-DS 32-query serial-only set from M0141-S1
after). This is now a genuinely small, well-derived, bounded change — not
"surgery" — per this loop's scoping. Do NOT attempt M0141-S2a-fix2 (the
currency correction) before fix1 lands and is measured in isolation. Also
still open and cheap: M0139-0007a (measure the two already-built
R108/R113 arms) and M0139-0007b (port Memoize's currency) from last loop's
recon, if the banner ranks those ahead of M0141-S2a-fix1 for any reason.
