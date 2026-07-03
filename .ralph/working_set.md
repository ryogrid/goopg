Task: M0110-0001 DU-002 slice 441 — `ALTER STATISTICS ... RENAME TO / OWNER TO /
SET SCHEMA` were silent no-ops, now apply (parser Action-switch + catalog
mutators + pg_statistic_ext owner/namespace fix). Work already implemented by
a prior loop; this loop is verifying + committing it.

Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/parser/alter_test.go, internal/executor/operators_ddl.go,
internal/executor/alter_statistics_test.go (new), internal/catalog/catalog.go,
docs/design/0110-0001-pg-dump-tap-port.md, docs/design/README.md,
.ralph/fix_plan.md, .ralph/deferral_ledger.md, .ralph/progress.json.

Key symbols: AlterStatisticsStmt.Action/NewName/NewOwner/NewSchema,
execAlterStatistics, catalog.StatisticsObject.Owner/OwnerOrDefault,
RenameStatisticsObject/SetStatisticsOwner/SetStatisticsSchema.

Findings: go build ./... clean. Named guard tests
TestParseAlterStatisticsRenameOwnerSetSchema (parser) and
TestAlterStatisticsRenameOwnerSetSchema (executor) both PASS. Full
internal/parser + internal/catalog + internal/executor suites PASS (no
regressions). scripts/tpch-spotcheck.sh launched in background (task
b763bud8f / Monitor bzzmzuac4) — awaiting canonical Q12=2/Q13=35 result
before committing per feedback_tpch_pre_commit_gates.

Next step: once tpch-spotcheck confirms PASS, `git add` the 12 changed/new
files (NOT `postgres` — pre-existing unrelated untracked dir, leave alone)
and commit with a message summarizing slice 441, matching repo commit style
(`fix(M0110-0001): ... (DU-002 slice 441)`). Then push per VCS rules. If
tpch-spotcheck FAILS or SKIPs, fall back to manual fresh-restart + Q12/Q13
spot-check steps in the practice card before deciding whether to commit.

Gates run this loop: go build (PASS), go vet implied by build, parser/
catalog/executor unit suites (PASS), tpch-spotcheck.sh (IN PROGRESS).
