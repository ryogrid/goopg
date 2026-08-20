Task: M0134-0052 (partition_join.sql) — PARKED this loop (sizing only, no code
fix). CSV row unchanged (`failed`). Next: select M0134-0053 (partition_prune.sql).

Files this loop: `.ralph/deferral_ledger.md` (new row, M0134-0052 bucket
breakdown), `.ralph/fix_plan.md` (M0134-0052 entry rewritten with PARK verdict
+ next-task pointer), `.ralph/progress.json` (state-guard auto-repair, same
recurring status/progress reconciliation as every prior loop — not new).

Key symbols / findings: `partition_join.sql` sizing (researcher, single round)
found the failure diff (6275 lines, 0/1 PASS) is ~95% one root cause:
partition-wise join is entirely unimplemented in `internal/optimizer` — goopg
always plans a plain `Append`-of-all-children joined against the other side,
never per-partition-pushed joins. PG oracle: `postgres/src/backend/optimizer/
path/joinrels.c:1422 try_partitionwise_join`, `allpaths.c:4362
generate_partitionwise_join_paths`, GUC `enable_partitionwise_join`
(`guc_tables.c:942`). That alone is a genuine new optimizer subsystem
(partition-bound matching + per-partition RelOptInfo synthesis + Append-of-
joins path construction) — same "needs a large unimplemented subsystem"
category as the already-parked M0134-0008/0009/0010. Four smaller independent
gaps also surfaced (parenthesized-join scoping bugs in
`internal/parser/select.go` `tryParseParenJoin`/`isSubqueryStart` — 2 distinct
failure modes; unimplemented `TABLESAMPLE`; an expression-keyed LIST-partition
INSERT routing failure). None of them, even fixed together, would flip this
test's pass/fail status since bucket 1 dominates — so no PARTIAL-FIX slice
existed here, unlike M0134-0049/0050. Full detail: deferral ledger row dated
2026-08-21, M0134-0052.

Hypothesis/Findings: paren-join scoping (buckets 2/3) may be higher-leverage
than this one file suggests — flagged as an open risk in the researcher's
report (untraced: how many OTHER regress-suite files rely on the CURRENT
buggy subquery-wrapping behavior of `tryParseParenJoin`). Not investigated
this loop; worth a `grep -rln JOIN` sweep across regress .sql files before
ever scoping a bucket-2/3 slice, since a naive fix risks regressing other
already-passing cases.

Next step: select **M0134-0053 (partition_prune.sql)** per the fix_plan
task-ID-ascending selection rule. Size it via `scripts/pg-regress-runner.sh
--verbose partition_prune` (delegate to researcher) before deciding
fix/split/park, same pattern as M0134-0044..0052.

Gates run this loop: `make ralph-state-guard` — ran clean after one
auto-repair (status/progress reconciliation, same recurring pattern as every
prior loop, not new). No engine code changed this loop (sizing/bookkeeping
only), so no build/test gate was needed; pgbench smoke will still run as part
of the pre-commit hook (mandatory on every commit regardless of file scope).

Delegation: researcher agent `a16a90fb7a698a49c` (1 round, sizing, found 5
buckets + PG oracle citations, recommended PARK — accepted as-is, no
follow-up round needed).

In-flight: none. No server left running (regress runner self-starts/stops its
own throwaway goopg instance via the cgroup wrapper). About to commit
`.ralph/deferral_ledger.md`, `.ralph/fix_plan.md`, `.ralph/progress.json`,
`.ralph/working_set.md` and push to `regress-renumbering`.
