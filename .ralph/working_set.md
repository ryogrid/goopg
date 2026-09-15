Task: M0141-S1 — "serial Hashed-vs-Sorted audit (selectable now)" for the
AGGSPLIT scoping programme. **DONE, committed this loop** (branch
`plan-parity-with-pg-take2-ralph`). No production code changed (recon-only,
matching its own mandate — confirmed `go build ./...` clean before commit).

Files: `docs/design/0100-0149/m0141-s1-serial-aggstrategy-audit.md` (new
design doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0141-S1 checked off with findings; S2 rewritten to open with S1's
per-query trace), `analysis/m0141/` (6 committed capture artefacts:
`m0141-s1-tpch{,.plans,.pg.plans}.txt`, `m0141-s1-tpcds-{goopg,pg,diff}.txt`,
~1MB total).

What was found (headline): **Action 1** — the `plan.go:1343-1349` comment
("planner does not set Strategy yet") is stale/wrong, not a real
contradiction to resolve by code change. Traced end to end:
`groupingpaths.go:addGroupingPaths` already runs a genuine cost-based
Hashed-vs-Sorted `PathAgg` contest via `addPath` (not a placeholder) ->
`createplansimple.go:createAggPlan`/`createFinalizeAggPlan` copy the
winner's `AggStrategy` onto the executor node (`out.Strategy =
p.AggStrategy`) -> `operators_join_agg.go:2222`'s `openSorted` dispatch
guard's `Mode == AggModeSimple` clause is unconditionally true under
`-serial=true`. Serial path is wired with NO missing link. Open question for
S2: WHY the contest doesn't pick PG's shape — a `costAgg` term disagreeing
with PG's `cost_agg`, or the Sorted candidate never generated
(`presortedAggKeysOrAbsent`/`groupingHasSpecialAgg` declining) — this task
did not narrow between those two, S2 must.

**Action 2** — fresh live captures (both bench cluster pairs were up:
:65432/:65433 TPC-H, :65437/:65438 TPC-DS SF0.25) via
`estimate-audit -plan-only` (TPC-H) and `capture-tpcds.sh` (TPC-DS), per
`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`. TPC-H:
confirmed by direct measurement (not just inference from the `-serial` flag
doc) that all 10 aggregation-strategy/9 sort-strategy tags are serial-shaped
(`parallelism=0` on both engines; match=6 floor held). TPC-DS: classified
each of 83 union-tagged queries by whether **PG's own reference plan**
(not goopg's, not the diff tool's label) contains a
`Partial`/`Finalize (Group)?Aggregate` or `Gather Merge` marker — 51 are
AGGSPLIT-touched (stays gated behind S0's entry gate + M0140's parallelism
floor), **32 are fully serial in PG's own plan** (in S1/S2 scope right now:
Q1, Q2, Q8, Q10, Q18, Q22, Q23, Q24, Q26, Q30, Q31, Q32, Q42, Q45, Q49, Q52,
Q53, Q54, Q55, Q56, Q59, Q60, Q63, Q69, Q71, Q80, Q81, Q83, Q91, Q92, Q93,
Q94; match=2 floor held). This is materially sharper than S0's "unseparated
mix" placeholder estimate. Caveat recorded in the design doc: the
classification is a plan-text marker search, not a structural proof the
marker's node is the query's own aggregate — S2 must re-verify per query it
actually works on, not trust the list blindly.

Key symbols: `groupingpaths.go:addGroupingPaths` (the existing Hashed/Sorted
`PathAgg` cost contest), `cost_funcs.go:costAgg` (the per-strategy cost
formula S2 must trace, spill-arm comment cites "R3" precedent),
`createplansimple.go:createAggPlan`/`createFinalizeAggPlan` (Strategy
copy-through, lines 172/219), `operators_join_agg.go:2222` (`openSorted`
dispatch guard), `presortedAggKeysOrAbsent`/`groupingHasSpecialAgg`
(groupingpaths.go — candidate-generation decline points S2 must check per
query), `scripts/pg-plan-parity-diff.py` (category tagging tool run this
loop), `scripts/capture-tpcds.sh` / `cmd/estimate-audit/main.go` (capture
tools, invoked per M0137-0003's documented command lines).

Gates run: `go build ./...` clean (no production code touched). Both live
bench-cluster captures completed with non-regression floors held (TPC-H
match=6, TPC-DS match=2 — AGENT.md's stated floor). Pre-commit hook (pgbench
smoke) expected to pass on `git commit` (not bypassed — run as part of
commit, not separately foreground-verified beyond the hook itself since no
server/executor code changed). `make ralph-state-guard`: run before this
status block (see below).
Nightly triage: all 14 open `AI-20260914-235643-*` items already filed under
M-NIGHTLY (re-verified this loop by grep against fix_plan.md, nothing new to
file — matches prior loop's count of 13 plus the previously-uncounted -001).

In-flight: none.

Next step: Select **M0141-S2** — "land the serial fix". First action: for
each of the ~10 TPC-H (all serial) and 32 TPC-DS serial-shaped queries this
task's design doc names, trace whether goopg's Sorted `PathAgg` candidate
was (a) generated and priced but lost the cost contest to Hashed, or (b)
never generated at all (check `presortedAggKeysOrAbsent`'s returned
`presorted` flag and `groupingHasSpecialAgg`'s decision for that query's
aggregate spec). Start with a small representative subset (e.g. TPC-H Q3,
Q4 — both `[aggregation-strategy,sort-strategy]` with no other category tag,
so the aggregate strategy is the ONLY divergence axis, cleanest signal).
Read `docs/design/0100-0149/m0141-s1-serial-aggstrategy-audit.md` before
starting — it names the exact hypotheses to distinguish and the query lists;
do not re-run the captures, they are committed under `analysis/m0141/`. Once
S2's trace identifies the mechanism, land the fix + the one-line
`plan.go:1343-1349` stale-comment correction in the same commit, per the
plan-parity harness's per-task gates (values gates: TPC-H digest, plan-gate,
sf025 sweep — see AGENT.md's "Success criterion").
