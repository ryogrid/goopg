Task: M0141-S7-exec-c — EXPLAIN rendering for *optimizer.IncrementalSort
(`Sort Key:` + PG's `Presorted Key:` line). LANDED AND COMMITTED this loop.
exec-a/b/c are now ALL LANDED for M0141-S7.

Files: internal/executor/operators_explain.go (extracted the *optimizer.Sort
case's per-key formatting loop into a new shared `sortKeyParts(child, keys,
reg, qualify) (full, bare []string)` helper; Sort case now calls it; new
`case *optimizer.IncrementalSort:` emits `Sort Key:` from `full` plus
`Presorted Key: ` + `bare[:PresortedCount]`), internal/executor/
operators_incremental_sort_build_test.go (+1 test,
TestExplainIncrementalSortPresortedKey). Docs: docs/design/0100-0149/
m0141-s7-readjudicate-and-scope-incremental-sort.md ("Update 2026-09-17g"),
docs/design/README.md (m0141-s7 row extended), .ralph/fix_plan.md (exec-c
checked off).

Key symbols: executor.sortKeyParts (new), the *optimizer.IncrementalSort
case in emitNodeDetailLines.

Findings: PG's show_incremental_sort_keys (explain.c:2583-2594) just calls
the SAME show_sort_group_keys a plain Sort uses, with nPresortedCols instead
of 0 — the function internally captures each key's pre-suffix exprstr (the
deparse output before show_sortorder_options appends DESC/COLLATE/NULLS)
and, if nPresortedKeys>0, emits its leading nPresortedKeys entries as a
second `Presorted Key:` property (explain.c:2816-2822) with NO
direction/NULLS decoration even for a DESC key — confirmed this is PG's own
oracle behaviour, not a goopg simplification, and pinned it in the new test
(`Presorted Key: grp` renders bare even though the full ORDER BY was
`grp, v DESC`). PresortedCount is always in (0, len(Keys)) by exec-b's own
createIncrementalSortPlan panic check, so PG's `if (nPresortedKeys > 0)`
guard always fires — no conditional needed in the new case. The 3
safe-to-decline sites named in exec-c's original scope (resolveKeySource,
childNodeOf, execParamOwnerChildren) were re-checked and are still correct
to leave declining.

Next step: M0141-S7-exec-d is deferred (ledger row already filed from
exec-b) — pending a corpus measurement (run the 14 TPC-DS witnesses with
GOOPG_INCREMENTAL_SORT=on) to see whether any actually needs sortOp's
spill/packed-retention/ctid/per-group-stat features before implementing
them. That measurement is the next open action in M0141-S7, but it's not
necessarily the next loop-sized pick — re-read the `## Current Priority`
banner in .ralph/fix_plan.md fresh next loop (it may have moved on to
M0141's other slices S2b/S3-S6, or M0142) and re-read AGENT.md's
plan-parity harness section again before selecting (required every loop
touching M0137-M0143).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` both green (includes the new test).
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
failure is the pre-existing, already-tracked `internal/parser`
GroupedJoinUnaliased AST-drift issue (optimizer/executor both `ok`,
confirmed untouched). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench
schema still not loaded, CLAUDE.md's M0142-0003k blocker, pre-existing/
unrelated; moot regardless, GOOPG_INCREMENTAL_SORT confirmed unset(off) in
the skip output). Nightly triage: all 17 AI- items from
ci/logs/action-items.md's 20260917-004357 run already had fix_plan.md
entries before this loop (confirmed by grep — no new filing needed this
loop). Pre-commit hook's pgbench smoke: pending (runs automatically on the
commit this loop makes). `make ralph-state-guard`: pending, run before the
status block.

In-flight: none.
