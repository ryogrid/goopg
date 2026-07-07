Task: M0122 follow-up — `pg_get_serial_sequence()` real OWNED-BY dependency
lookup (unimplemented_feat.json entry, also flagged by M0122-0002's own
summary line which had wrongly claimed it "already implemented, verified
2026-07-04"). COMPLETE and committed this loop (`fdae8b13`).

Files: internal/executor/expr.go (the `pg_get_serial_sequence` case — was a
stub fabricating `public.<table>_<col>_seq`; now strips a schema qualifier
off the table arg, calls `FindSequenceOwnedBy(bareTable+"."+col)`, returns
NULL when unowned, else schema-qualifies via the sequence's own
`seqState.schema` + shared `pgQuoteIdent`).
internal/executor/operators_pg_get_serial_sequence_test.go (new: serial
column resolves; non-serial column → NULL; renamed sequence followed;
identity column resolves).
unimplemented_feat.json (surgical 2-field edit only, per house rule —
entry's `status`→resolved, `code_audit` rewritten; verified valid JSON via
`python3 -c "json.load(...)"` after, did NOT run json.load+json.dump).
.ralph/deferral_ledger.md (new row: 2 residual gaps recorded — (1)
`FindSequenceOwnedBy`'s exact-string match can miss an explicit
`ALTER SEQUENCE ... OWNED BY myschema.tbl.col` since the explicit-DDL path
stores the parser's raw text unnormalized while the implicit SERIAL/IDENTITY
path always stores bare `table.column`; (2) this function returns NULL
instead of raising 42P01/42703 for a nonexistent table/column, deliberately
matching this file's sibling pg_get_* convention, not upstream PG).
docs/design/root-0020-sequence-serial-restart-persistence.md (new "Follow-up
(2026-07-08)" section — this is the general sequence/SERIAL design doc, most
relevant home for this fix) + docs/design/README.md (root-0020 row addendum).
.ralph/fix_plan.md (M0122-0002 bullet: appended a "Correction (2026-07-08)"
paragraph noting/fixing the stale "already implemented" claim).

Key symbols: `FindSequenceOwnedBy`/`SetSequenceOwnedBy` (operators_sequence.go,
pre-existing, now has a second real caller); `pgQuoteIdent` (expr.go, shared
identifier quoter); the `pg_get_serial_sequence` case in expr.go's big
`evalExpr` function-name switch.

Findings: confirmed via `git stash` on expr.go alone that 2 of the 4 new
tests fail on pre-fix code exactly as predicted (non-serial column wrongly
returned a guessed name; renamed sequence wrongly returned the stale
pre-rename name) — non-vacuous. `FindSequenceOwnedBy`/`SetSequenceOwnedBy`
already existed and were already correctly wired for SERIAL/IDENTITY column
registration (operators_ddl.go:2870) and explicit `ALTER SEQUENCE ... OWNED
BY` (operators_ddl.go:13350/13588) — this task only needed to add a second
consumer in expr.go, not build new tracking infrastructure.

Next step: pick a fresh M0122 item next loop. Un-investigated candidates
noted by prior loops: M0119-0006's opclass-dispatch remainder (pg_amproc
Virtual-UPDATE path mirroring `nextVirtualPgDatabase`, PLUS btree opclass/
comparator dispatch — explicitly flagged "not a single-loop slice" 3 times
now in the deferral ledger; decompose further, e.g. just the pg_amproc
Virtual-UPDATE mutator alone, before attempting). Also worth a fresh look:
M0122-0001's now-complete backlog triage (181/181 tagged) means the
remaining pool of individually-pickable `open` entries in
unimplemented_feat.json is now the primary task-selection source — grep for
`"status": "open"` and pick one with `confidence: "high"` and no cross-
subsystem architecture dependency (avoid picking another one already flagged
as multi-loop, like the btree opclass-dispatch one above).

Gates run: go build ./... clean. go vet ./internal/executor/... clean.
go test ./internal/executor/... (full package, 3.9s) PASS. go test
./internal/parser/... ./internal/planner/... ./internal/catalog/... PASS
(cached, unaffected packages). scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0 failed,
all 3 workloads: TPC-B, simple-update, select-only) — run twice, once
standalone pre-commit-check and once as the actual git pre-commit hook.
make ralph-state-guard: 1 benign issue auto-repaired (same recurring
status/progress clean-exit-vs-in_progress reconciliation as every prior
loop — not a new problem, do not chase it further).

In-flight: none directly mine. Noticed (informational, not mine to manage):
the nightly-batch catch-up run `ci/logs/20260708-064334` (started this
morning per [[goopg_nightly_ci_batch]] memory's independent scheduler infra,
sha=2e435e91, dirty=12) was still running the race stage as of this loop's
start and had NOT yet reached pgbench/tpch/summary. Confirmed via
`git merge-base --is-ancestor` that this run's base sha DOES include the
flushBatch stale-tag fix (`8ebb71cd`, M-NIGHTLY pgbench/nightly item's real
root-cause fix, landed loop #17 of that investigation) — so once this run
completes, its pgbench stage is the real confirmation the M-NIGHTLY
`pgbench/nightly` bullet in fix_plan.md is waiting on. Next loop's nightly-
triage step should check `ci/logs/latest/stages/pgbench/` (or wherever that
stage lands) for a clean result and, if clean, check off/archive that bullet
per its own "next nightly run is the real confirmation" note (fix_plan.md
line ~624). If `ci/logs/action-items.md` was regenerated with a NEW mtime by
the time this run finishes, re-run the standard nightly-triage step (grep for
`## AI-` items lacking an open fix_plan task) before picking new work.
