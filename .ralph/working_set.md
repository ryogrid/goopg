Task: M0122-0007 residual — `ALTER TABLE ... ALTER COLUMN col RESET (...)`
(unimplemented_feat.json M0110-0001 entry, also named in M0122-0007's "still
open" list). COMPLETE and ready to commit this loop.

Files: internal/parser/ast.go (new `AlterTableAlterColumnReset` action kind;
extended `SetOptions` field doc to note it's shared with SET).
internal/parser/ddl.go (new RESET (opt, …) branch in the ALTER TABLE ALTER
COLUMN dispatch, right after the existing SET (...) branch; reuses
`parseColumnSetOptions` since it already accepts bare option names).
internal/parser/ddl_test.go (new `TestParseAlterTableAlterColumnReset`).
internal/executor/operators_ddl.go (new `AlterTableAlterColumnReset` exec
case — merges named keys OUT of `catalog.Column.Options`, mirrors upstream
`ATExecResetOptions`'s partial-clear semantics, NOT a wholesale wipe; reuses
the same pg_attribute heap-resync path the sibling SET case already
established; also extended the pre-existing "not supported for indexes"
0A000 guard to cover RESET, not just SET).
internal/executor/operators_ddl_alter_column_reset_test.go (new
`TestAlterColumnResetOptionsClearsNamedEntriesOnly`).
unimplemented_feat.json (surgical 2-field edit only, per house rule: this
entry's `status` → resolved, `code_audit` updated with the fix write-up —
did NOT run json.load+json.dump, verified valid JSON via python3 -c
"json.load(...)" after).
docs/design/0110-0001-pg-dump-tap-port.md (new "Follow-up: ALTER TABLE ...
ALTER COLUMN col RESET (...)" section) + docs/design/README.md (row 574
addendum appended in place, not rewritten).
.ralph/fix_plan.md (M0122-0007 bullet: added a FIXED follow-up paragraph,
trimmed "~13 remaining" → "~10 remaining" in the still-open list since
RESET is done).

Key symbols: parser.AlterTableAlterColumnReset (ast.go); the RESET branch in
ddl.go right after `if p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet)
{ ... }` in the ALTER COLUMN dispatch (~line 8551 pre-edit); the exec case
in operators_ddl.go right after `case parser.AlterTableAlterColumnSet:`'s
block (~line 8062 pre-edit).

Findings: confirmed the gap was real (internal/parser/ddl.go's ALTER COLUMN
dispatch had zero RESET handling — fell to the generic no-op consume loop).
Confirmed KindInterval (the executor's interval Datum) has NO sub-day
component at all (months+days only, by design per
docs/design/0003-0006-date-interval-arithmetic.md) — this RULES OUT
"timestamp - timestamp → interval" (unimplemented_feat.json entry #4) as a
comparably-small follow-up candidate for a future loop; implementing it
faithfully needs a genuinely new microseconds field on the interval Datum
(ripples through Format/compareDatum/cast), not a single-function fix. Do
not pick that one without budgeting a full loop for the Datum extension.
Confirmed non-vacuous via `git stash` on the parser+executor changes: the
executor test fails pre-fix with `Options = [n_distinct=0.5
n_distinct_inherited=-0.1]` (both entries survive RESET) exactly as
predicted.

Next step: pick the next M0122 item fresh next loop. Good remaining
candidates surfaced this loop (NOT yet started): `pg_get_serial_sequence()`
real catalog-dependency lookup instead of the convention-based
`table_col_seq` name fabrication (`internal/executor/expr.go:~7325`,
unimplemented_feat.json entry #57) looked similarly well-scoped but was not
independently verified this loop — verify-before-implement per M0122's
own rule before starting it. Also M0119-0006's opclass-dispatch remainder
(pg_amproc Virtual-UPDATE path + btree opclass/comparator dispatch,
unimplemented_feat.json's "pg_amcheck server-dependent test tiers" entry) —
explicitly flagged "not a single-loop slice" twice now; decompose further
before attempting (e.g. just the pg_amproc Virtual-UPDATE path alone,
mirroring nextVirtualPgDatabase, might be a viable single slice even though
it wouldn't close 005_opclass_damage.pl on its own).

Gates run: go build ./... clean. go vet ./internal/parser/...
./internal/executor/... clean. go test ./internal/parser/...
./internal/executor/... ./internal/planner/... ./internal/catalog/... PASS.
Full go test over all internal/... packages (excluding /testport and
/testutil/tpch per project convention) PASS, including internal/initdb's
full suite (399s) and internal/server (8.9s). scripts/tpch-spotcheck.sh PASS
(Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh
PASS (0 failed, all 3 workloads: TPC-B, simple-update, select-only).
make ralph-state-guard: 1 benign issue auto-repaired (same recurring
status/progress clean-exit-vs-in_progress reconciliation as every prior
loop — not a new problem).

In-flight: none. Noticed (informational only, not mine to manage): the
nightly scheduler fired a fresh batch run (20260708-064334) partway through
this loop, apparently because the scheduler process restarted around
01:37 and re-fired the missed 2026-07-08 00:00 slot late at 06:43 — it
snapshotted the tree mid-edit (dirty=12). This is independent background
infra (ci/batch clones into its own runtime dir per
[[goopg_nightly_ci_batch]] memory); no action needed from this loop, but
the next loop's nightly-triage step should check whether
ci/logs/action-items.md got regenerated by this run and whether its
"dirty" snapshot produced any spurious findings worth discounting.
