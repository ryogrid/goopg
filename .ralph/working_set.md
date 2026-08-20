Task: M0134-0048 (create_view.sql, status `failed`) — sized, harness bug fixed,
engine case still PARKED (needs design pass).

Files this loop: `scripts/pg-regress-runner.sh` (real code fix — harness
self-collision guard), `.ralph/deferral_ledger.md` (new row, M0134-0048, full
bucket table), `.ralph/fix_plan.md` (M0134-0048 entry rewritten to PARKED,
points next selection at M0134-0049), `.ralph/progress.json` (state-guard
auto-repair, unrelated bookkeeping).

Key symbols / findings: `scripts/pg-regress-runner.sh`'s `RUN_SETUP`
prerequisite phase (~line 218) unconditionally re-runs
`create_misc.sql`/`create_index.sql`/`create_view.sql`/`create_aggregate.sql`
even when one of those is ALSO the requested named test — the named-test run
then executes the SAME file a second time against the same live DB, producing
spurious "already exists" cascade noise. Added `is_named_test()` guard (loops
`NAMED_TESTS[@]`, populated by line 151, well before the setup block) so a
colliding prerequisite is skipped with an explanatory echo instead. Verified:
`create_view` diff 2756→2505 lines / 152→50 `^+ERROR`; `select_views`/`int2`/
`int4` (non-colliding names) unaffected — all four prerequisites still run
normally. This is cross-cutting: sizing accuracy improves for ANY future
M0134 task literally named `create_misc`/`create_index`/`create_view`/
`create_aggregate`.

Post-fix real diff for `create_view.sql` (50 `^+ERROR`) is dominated (~80%)
by a genuine subsystem gap: goopg has NO `ruleutils.c`-equivalent view/rule
deparser — `pg_get_viewdef()`/`pg_get_ruledef()`
(`internal/executor/expr.go:8970-9018`) echo the raw CREATE VIEW SQL text
captured at parse time instead of PG's canonicalized re-derivation from the
analyzed Query tree (confirmed live: `ALTER TABLE ... RENAME` doesn't
propagate into a dependent view's stored body). PG oracle:
`postgres/src/backend/utils/adt/ruleutils.c` (`get_query_def` etc, ~7000
lines, no goopg counterpart). Smaller CONTAINED-ish gaps found alongside:
(i) `CREATE SCHEMA name CREATE TABLE ...` nested schema-elements silently
dropped (no `CreateSchemaStmt.Elements` AST field anywhere in
`internal/parser`, `internal/postmaster/dispatch.go:1896-1936` intercepts
standalone CREATE SCHEMA before element lists parse) — cascades into ~20
later `CREATE VIEW ... FROM base_table` failures; (ii) temp-view
auto-promotion missing (`execCreateView`,
`internal/executor/operators_ddl.go:5940`); (iii) view WITH-options
validation gaps (security_barrier/security_invoker boolean-value checking,
unrecognized-option rejection).

Hypothesis/Findings: mirrors the M0134-0045/0047 storage/subsystem-gap
deferral pattern — the deparser bucket needs a `docs/design/` scoping pass
(also backs `pg_get_ruledef`/`pg_rewrite`, must be designed together) before
an implementer slice. CREATE SCHEMA nested elements is a separate, smaller,
reusable CONTAINED task worth picking up independently of the deparser work.

Next step: select **M0134-0049 (numeric.sql)** per the fix_plan
banner/entry chain — size it via `scripts/pg-regress-runner.sh --verbose
numeric` (delegate to researcher) before deciding whether it's a
diff-mismatch, crash, or feature-gap case, following the same
research→brief→(implement|park) pattern used across M0134-0044..0048.

Gates run this loop: `make ralph-state-guard` — ran clean after one
auto-repair (status/progress reconciliation, same as prior loops, unrelated
to this loop's work); pgbench smoke PASS (pre-commit hook, mandatory);
implementer verified `bash -n` syntax + 4 live `pg-regress-runner.sh` runs
(`create_view`, `select_views`, `int2 int4`) — all passed acceptance
criteria.

Delegation: researcher agent `ad6bdda7691def8c4` (1 round, sizing) →
implementer agent `a29fe7bad995524e4` (1 round, harness fix, DONE first
try). Handoff: `tmp/ralph-handoffs/m0134-0048-harness-setup-collision/`
(brief.md written; implementer report folded directly into this file + the
ledger row since its own Write of report.md was blocked by a tool
restriction — no data lost, captured verbatim in the commit history of this
turn).

In-flight: none. No server left running (script self-starts/stops its own
throwaway goopg on port 15435 each invocation). Tree clean pending this
loop's commit.
