Task: M0142-0014 — triage the 17 TPC-DS plan-shape changes M0142-0012-verify's
self-diff found. **DONE and committed** this loop. Follow-up filed and NOT
started: M0142-0015 (Q45's DP-search tie-flip root cause).

Files: `docs/design/0100-0149/m0142-0014-triage-17-tpcds-shape-changes.md`
(new), `docs/design/README.md` (indexed), `.ralph/fix_plan.md` (0014 `[x]`,
0015 filed `[ ]`), `.ralph/deferral_ledger.md` (new row). No `analysis/`
artefacts needed — reused M0142-0001's and M0142-0012-verify's
already-committed captures, no re-capture.

Key symbols: none touched — pure recon, zero production Go diff (`go build
./...` unaffected). Method worth remembering: for a per-query "did this
change help or hurt" triage, diff each query's `pg-plan-parity-diff.py`
category-*tag set* (not the cost numbers) pre/post — subset-of-pre = neutral/
improved, gained-a-new-tag = candidate regression — then cross-check the sum
of all per-query deltas against the tool's own aggregate `CATEGORIES` line
delta. If they match exactly (they did here, residual-free), the
classification is self-consistent, not just plausible.

Findings this loop: 17 = 13 neutral (tag set literally unchanged) + 3
improved (Q31 -3, Q54 -2, Q72 -2 divergent tags) + 1 genuine regression (Q45,
+3: join-order, scan-type, qual-placement). This exactly attributes every
category delta M0142-0012-verify measured (join-order +1 solely Q45,
join-method -1 solely Q54, aggregation-strategy/sort-strategy -1 solely Q31,
parallelism -2 Q31+Q54, scan-type/qual-placement net 0 as Q45's +1 cancels
Q72's -1) — the "moved sideways" wash was one real regression cancelling
three real improvements, not a taxonomy artefact. Confirmed Q45 by reading
raw EXPLAIN text directly (not just tags): pre-M0142-0012 goopg's join order
for Q45 (`...⋈customer⋈customer_address⋈item`) matched real PG 18.3's own
order bit-for-bit; post-fix it's `...⋈customer⋈item⋈customer_address` — the
`item`/`customer_address` pair swapped. Both are unmemoized index-probe
nested loops of the exact decomposed-NLI shape M0142-0012 just re-priced, so
this reads as a close DP-search tie flipping once one leg's cost became more
accurate — unverified which ordering is actually *more* accurate; that's
M0142-0015's job. **Net verdict: M0142-0012 remains a genuine corpus
improvement** (3 wins vs 1 now-attributed, non-mysterious loss); Q45 was
already non-matching pre-fix so nothing crossed from `match` to `shapediff`.

Next step: re-read `.ralph/fix_plan.md`'s `## Current Priority` banner first
(precedence rule) — as of this loop's start it still points to M0142 item 4
(M0141 remaining slices / M0142 costing). Within M0142, open items are now:
M0142-0003c (level-6 enumeration-order parity, Q9), M0142-0005 (Memoize/
probe-multiplier interlock, the biggest lever — Q72 timeout + several
ea-ratchet findings depend on it), M0142-0008 (recon: how much of goopg's
plan shape is forced-rewrite vs search), M0142-0013 (5 NEW ea-ratchet
findings from M0142-0012, Q23/Q84/Q95 — same size/shape as this loop's
M0142-0014, still unstarted), M0142-0015 (this loop's own follow-up, Q45).
M0142-0005 is flagged in the banner text as carrying the most corpus
evidence now (M0142-0005's own entry cites M0142-0010's Q34/Q73 findings) —
reasonable next pick if the banner still ranks M0142 items by "biggest
lever first," but M0142-0013/0015 are smaller, cheaper, already-scoped
recons if a shorter loop is preferred. Check banner wording exactly before
choosing; it may have been rewritten since this baton was written.

Gates run: `go build ./...` clean (no source touched). This was a doc/
markdown-only, measurement-only loop — no unit/integration/parity gate
applies (nothing executable changed). `make ralph-state-guard`: run
immediately before the status block per protocol, not yet executed as of
this baton write.

In-flight: none. No server was started this loop (all evidence came from
already-committed `analysis/m0142/` capture files, no live cluster touched).
Pre-existing unrelated uncommitted changes in the tree at loop start
(`.claude/settings.json`, `.ralph/progress.json`,
`analysis/tpch-explain-baseline.md`, `ci/logs/launch.log`,
`ci/logs/scheduler.log`, plus various untracked `bench/tpcds/runtime_goopg/`
and `analysis/leftdeep-joins/` files) were left untouched and are NOT part of
this loop's commit — staged this loop's own files by explicit pathspec only.
