Task: M0137-0016 — add the `*Gather` arm to `pushConjunctTraced` (banner's
item 2, "M0137's re-opened tasks 0014-0017"). **DONE this loop** (`679d77d11`,
committed). Closes O15, the no-owner ledger row M0137-0012 filed.

Files: `internal/optimizer/inner_join_qual_pushdown.go` (`pushConjunctTraced`
gained a `*Gather` case — bare pass-through recursion into `x.Child`,
`st.proven` left untouched), `internal/optimizer/pushdown_gather_crossing_test.go`
(new, 3 tests mirroring `pushdown_project_crossing_test.go`'s pattern),
`docs/design/0100-0149/m0137-0016-gather-arm-pushconjuncttraced.md` (new),
`docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0137-0016 checked
`[x]`), `.ralph/deferral_ledger.md`
(`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced` flipped to
`resolved`).

Key symbols: `pushConjunctTraced`'s `switch x := n.(type)`
(inner_join_qual_pushdown.go:341, new `*Gather` case inserted just before the
existing `*Join` case), `Gather.Output()`/`NewGather`
(plan.go:2733-2760, confirms `Output() == Child.Output()`, i.e. no
coordinate shift — why the new arm needs none of the `*Join` arm's
left/right math or the `*Project` arm's remap-and-fail-closed logic).

Findings: measured corpus-wide blast radius from the committed PG-oracle
plan captures (`bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/` — no server
needed, a Python indent-scan for a `Filter:` line at-or-below a `Gather`
node's indent before the next dedent): **TPC-H 0/22** queries place a filter
below a Gather in PG's own reference plans; **TPC-DS 37/99** do. No
TPC-DS category-count claim was made — M0140-0003's `GOOPG_GATHER_PATHS=all`
experience (category count moved 525→540 net-worse while `match` held) is
the standing caution that a parallel-boundary fix often surfaces *new
visible* divergence rather than moving categories, so this loop reported
only the instrument-gap closure and the measured PG-side shape count, not a
predicted TPC-DS effect.

In-flight: none. No server/cluster touched this loop beyond the mandatory
`scripts/tpch-spotcheck.sh` gate (private port 5580, clone
`tmp/goopg-spotcheck-tpch-data`, shared `:65433` never touched) and the
pre-commit hook's pgbench smoke (its own private throwaway data dir).

Next step: re-read the `## Current Priority` banner fresh in
`.ralph/fix_plan.md` (check date/content before trusting this note). Per the
banner as last read (2026-09-15 "Re-ordered" section), item 2 ("M0137's
re-opened tasks 0014-0017") is now 2-of-4 done (0015, 0016). The remaining
two: **M0137-0014** (automate the seam-decline census — a new `scripts/`
tool that runs `GOOPG_PGSHAPED_DP_TRACE=1` and emits per-class decline
counts with the timeout stamped, sibling of the already-landed
`CATEGORIES-EXCL-MATCH:` fix) and **M0137-0017** (capture TPC-H plans in
BOTH `-serial` and `-serial=false` modes against separate PG baselines —
flagged as mattering more than its size suggests, since `parallelism` is
currently unscoreable in the TPC-H 6/22 headline; building the
parallel-mode PG baseline is part of this task, `bench/tpch/plans-pg/` is
serial-only today). Pick whichever the banner still ranks next; if
unchanged, 0017 looks higher-value (unblocks a whole unscoreable category)
but is larger (needs a new PG baseline capture) — 0014 is the smaller,
faster pick if the loop budget is tight.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` full
package `ok` (includes the 3 new tests). `go test ./internal/executor/...
-run Explain` `ok`. `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=34,
canonical; private port 5580). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: `internal/optimizer` and
`internal/executor` both `ok`; `internal/parser` FAILed on the SAME
pre-existing, already-filed `GroupedJoinUnaliased` AST-drift issue prior
loops confirmed is unrelated to this diff (not this loop's to fix — filed
in fix_plan.md under "Manually discovered... filed 2026-09-15"). Pre-commit
pgbench smoke PASS (hook-enforced; tps ~43-150, 0 failed). `make
ralph-state-guard`: one self-repair (same recurring benign
stale-clean-exit-marker pattern several prior loops have noted), clean
after repair.
