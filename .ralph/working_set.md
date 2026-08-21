Task: M0134-0069 (sequence.sql) — Bucket 4 now FULLY landed (both explicit-
and implicit-DEFAULT halves). Case still `failed` (0/1, diff 330→307 lines
this loop). Committed & pushed (8ee2f743).

Files this loop: `internal/catalog/catalog.go` (new
`FindSequenceOwnedByFunc` function-var hook, sibling to `SequenceParamsFunc`),
`internal/executor/operators_sequence.go` (`init()` wires
`catalog.FindSequenceOwnedByFunc = findSequenceOwnedByForCatalog`, new helper
mirrors `autoGenerateSerialValues`'s naming-convention→`FindSequenceOwnedBy`
two-step lookup), `internal/optimizer/planner.go` (`defaultMarkerReplacement`
~line 10025 now calls the hook to resolve the CURRENT/renamed sequence name
for implicit SERIAL/IDENTITY DEFAULTs instead of the dead naming-convention
literal; stale doc comment updated), `internal/executor/m0134_0069_test.go`
(new `TestAlterSequenceRenamePropagatesToImplicitSerialDefault`),
`.ralph/deferral_ledger.md` (new row, M0134-0069 dated 2026-08-21 — records
6 residual sub-buckets), `.ralph/fix_plan.md` (M0134-0069 entry updated,
still unchecked — not all buckets done).

Key symbols: `catalog.FindSequenceOwnedByFunc` (catalog.go, right after
`SequenceParamsFunc`); `findSequenceOwnedByForCatalog`
(operators_sequence.go); `defaultMarkerReplacement`/
`rewriteInsertDefaultMarkers` (planner.go ~10025, now rename-survival-safe).

Hypothesis/Findings: the case as a whole is still `failed` — Bucket 4 was
only one of 6 sizing buckets. Remaining, per the ledger: Bucket 3 (DROP
SEQUENCE RESTRICT missing column-default dependency check — researcher
agent a9606871a3f807476 already confirmed the parser-cast risk is REAL: PG
only records a dependency for a bare-literal or `::regclass`-cast `nextval`
arg, never `::text`; safe to start coding without further research), Bucket
5 (sequence ACL/owner enforcement — `nextval`/`currval`/`lastval`/`setval`
in `internal/executor/expr.go` ~10145-10176 never call the existing
`dmlPrivilegePermittedAs` helper), Bucket 6 (small text/HINT/DETAIL gaps),
plus newly-surfaced-this-loop small items: `pg_sequence_parameters()` SRF
missing, `\d` doesn't label UNLOGGED sequences, orphaned `sequence_test2`
catalog row after cascading DROP TABLE (wrong min/start), `pg_get_sequence_data`
after CACHE 10 returns 3 not 10 (cache-vs-persisted mismatch, not yet
root-caused).

Next step: start Bucket 3 (DROP SEQUENCE RESTRICT dependency scan) — the
parser-risk research is already done, so this can go straight to an
implementer brief: clone the `viewsDependingOnTable` pattern in
`internal/executor/operators_ddl.go`'s DROP SEQUENCE handler (~line
19825-19875, currently only checks function deps via
`functionsDependingOnSequence`) to also scan `im.AllTables()` column
`DefaultExpr`s (plus implicit-serial synthesized names) for `nextval(arg)`
where `arg` is a bare `StringConst` or `::regclass`-cast — NOT `::text` or
other casts — matching the OLD sequence name; emit PG's `2BP01` error with
DETAIL `default value for column "X" of table "Y" depends on sequence "Z"`.
Acceptance pair: `sequence.sql:151-167` (`myseq2`/`myseq3`/`t1_f1_seq`).

Gates run this loop: `go build ./...` PASS; targeted test PASS; full
`go test ./internal/executor/...` PASS; full `go test ./internal/optimizer/...`
PASS; `scripts/pg-regress-runner.sh --verbose sequence` — diff 330→307,
anchor line now matches PG; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (full suite incl. slow initdb/goopg
packages); `make ralph-state-guard` — found 2 stale markers, auto-repaired,
then PASS; pre-commit pgbench smoke PASS (11677 TPS select-only, 650 TPS
simple-update, 0 failed).

Delegation: researcher agent `a9606871a3f807476` (1 round — resolved the
implicit-serial fallback design AND confirmed Bucket 3's parser-cast risk
is real, both in one pass); implementer agent `ac9833ec36a9e4862` (1 round
— landed the full brief cleanly, no escalation needed, diff shrank as
expected).

In-flight: none. Commit `8ee2f743` pushed to `regress-renumbering`. No
server left running.
