Task: M0137-0015 — embed `PlanCost` in `optimizer.Filter` (banner's item 2,
"M0137's re-opened tasks 0014-0017"). **DONE this loop** (`4e25a872c`,
committed). Landed the one-line fix M0137-0011 root-caused but did not
implement.

Files: `internal/optimizer/plan.go` (`Filter` struct gained a `PlanCost`
embed, mirroring `SeqScan`'s), `internal/optimizer/createplan_test.go` (new
`TestCreatePlanNode_StampsCostOnFilterWrappedPrebuiltLeaf`, pins the
mechanism), `docs/design/0100-0149/m0137-0015-filter-plancost-embed.md` (new,
full verification), `docs/design/README.md` (+1 row), `.ralph/fix_plan.md`
(M0137-0015 checked `[x]`), `.ralph/deferral_ledger.md`
(`m0137-0011-filter-node-missing-plancost-embed` row flipped to `resolved`).

Key symbols: `Filter` (plan.go:1538), `stampPlanCost`/`legacyDisplayChildren`
(plancost.go — neither needed changing, the `*Filter` fallback arm already
existed and was simply unreachable), `scanLeafFor`'s `rewrap` closure
(createplanindex.go:158) — confirmed it produces the SAME `Filter` type, so
the `*IndexScan`-with-residual-filter case M0137-0011 flagged as unaudited
needed no separate fix (`IndexScan` already embeds `PlanCost`).

Findings: live-verified against a private `bench/tpch` clone (port 5581,
`tmp/goopg-spotcheck-tpch-data`, shared `:65433` never touched) — M0137-0011's
exact Q12 symptom (`Seq Scan on lineitem cost=0.00..60299.79`, the
`DeriveLegacyDisplayCost` fallback) now renders `cost=0.00..271421.46`, the
search's own `costSeqscan` number. An `awk` sweep of every `optimizer` struct
embedding `searchedTree` but not `PlanCost` found one other hit, `Project` —
not a `buildInitialRels`/`scanLeafFor` leaf shape, out of scope, noted in the
design doc for a later slice if one ever needs it. Corpus-wide blast radius
(M0137-0011 left this unmeasured): counted directly from the committed
PG-oracle plans (`bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/`) — 20/22
TPC-H and 85/99 TPC-DS queries carry at least one base-local-filtered scan,
so the majority of both corpora were rendering a corrupted EXPLAIN cost
column before this fix (corrupting `plan-gate MODE=semantic-cost` and every
estimate-audit table entry that reads a rendered scan cost).

In-flight: none. The private verify server (port 5581, scope
`goopg-m0137-0015-verify`) was stopped and its transient systemd scope
confirmed gone; the throwaway binary `tmp/goopg-m0137-0015-bin` was removed.
Shared clusters (`:65432`/`:65433`/`:65437`/`:65438`) confirmed still
listening, untouched throughout.

Next step: re-read the `## Current Priority` banner fresh in
`.ralph/fix_plan.md` (check date/content before trusting this note). Per the
banner as last read, item 2 ("M0137's re-opened tasks 0014-0017") is now
1-of-4 done (0015). The remaining three in that item: **M0137-0014**
(automate the seam-decline census — a new `scripts/` tool that runs
`GOOPG_PGSHAPED_DP_TRACE=1` and emits per-class decline counts with the
timeout stamped), **M0137-0016** (add a `*Gather` arm to
`pushConjunctTraced`/O15 — ledger row already carries the recipe, its stated
gate is satisfied), **M0137-0017** (capture TPC-H plans in both `-serial` and
`-serial=false` modes against separate PG baselines — flagged as mattering
more than its size suggests, since `parallelism` is currently unscoreable in
the TPC-H 6/22 headline). Pick whichever the banner still ranks next; if the
banner is unchanged, 0016 looks like the next quick win (small, gate already
satisfied) before tackling 0017's larger baseline-capture work.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` full
package `ok` (includes the new pinning test). `go test ./internal/executor/...
-run Explain` `ok`. `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=34,
canonical; private port 5580, clone never touched `:65433`). Manual
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` surfaced ONE
failing package, `internal/parser` (`TestLockingClauseParity` and ~60 other
functions, `GroupedJoinUnaliased` AST-drift) — reproduced it on a stash of
this loop's own diff too, confirming it is baked into HEAD already and wholly
unrelated to this task (different subsystem, no uncommitted diff in
`internal/parser`); it is already a filed, known issue in `.ralph/fix_plan.md`
under "Manually discovered... filed 2026-09-15" — not this loop's to fix.
Pre-commit pgbench smoke PASS (hook-enforced; tps ~43-147 across the three
builtin scripts, 0 failed). `make ralph-state-guard`: one self-repair (same
recurring benign stale-clean-exit-marker pattern several prior loops have
noted — status/progress reconciled, not a real inconsistency), clean after
repair.
