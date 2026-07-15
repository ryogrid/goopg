Task: M0122-0007 4e follow-up — `CREATE DOMAIN`'s `AS` base_type now accepts
multi-word built-in type names (fix_plan.md, appended right after the
`domains` cross-database follow-up which the prior loop landed, both directly
before "## Archived — complete (completed_fix_plan_009.md)"). COMPLETE and
committed (pending push — see Gates run).

Files: internal/parser/ddl.go (factored the CREATE-TABLE-only multi-word-
typename switch out of parseColumnType into two shared helpers —
parseMultiWordTypeName pre-typmod-args, parseTimeZoneQualifierAfterArgs
post-typmod-args — and wired both into parseCreateDomain's base-type parsing;
parseColumnType's own behavior unchanged, just relocated); internal/parser/
m0097_0017_test.go (8 new multi-word cases in TestM0097_0017_EnumDomainParsing
+ new TestCreateDomainMultiWordBaseType asserting BaseType/BaseTypeArgs);
docs/design/0097-0017-0001-enum-domain-types.md (new "Follow-up (2026-07-15)"
section) + docs/design/README.md (row updated); .ralph/deferral_ledger.md (new
row); .ralph/fix_plan.md (new [x] entry).

Key symbols: parser.parseMultiWordTypeName, parser.
parseTimeZoneQualifierAfterArgs, parser.parseCreateDomain, parser.
parseColumnType.

Hypothesis/Findings: M-NIGHTLY queue empty this loop (ci/logs/action-items.md
run 20260715-010036, all 11 items already [x] in fix_plan.md — confirmed via
grep, matches what the prior loop also found). Resumed exactly where
working_set left off: the DU-002 probe (TestPort_PgDumpConnectionSetup) was
blocked on `CREATE DOMAIN public.f8_in AS double precision` failing with a
parser syntax error. Root cause: parseCreateDomain used bare
parseObjectName() for the base type (schema.name only), never calling the
multi-word-typename switch that parseColumnType (CREATE TABLE) already had.
Fix was a pure refactor-and-reuse: extracted parseColumnType's switch logic
into 2 shared helpers, called them from parseCreateDomain too. No new
behavior in parseColumnType itself — verified via full local suite (0
regressions).
**Probe moved further, confirms the fix**: TestPort_PgDumpConnectionSetup now
PASSES (parses the double-precision domain fine) and logs a *different*,
already-expected failure via t.Logf (not a test failure): `type "gtype"
already exists` — a CREATE TYPE cross-database catalog-isolation collision,
the same collision class as the domains/userCollations fixes from the last 2
loops but for CREATE TYPE's user-defined-type registry (not investigated this
loop — parser grammar and catalog dbOid-threading are different mechanisms,
kept as separate bounded loops per the deferral ledger's stated policy).

Next step: A future loop should audit catalog.InMemory's CREATE TYPE /
user-defined-type registry (composite types + enums, likely a
map[string]*EnumType or similar keyed by bare name — same shape domains/
userCollations had before their fixes) for the same DBOid-less collision, then
apply the exact domainKey/lookupDomainByNameLocked pattern from the domains
fix (see .ralph/deferral_ledger.md 2026-07-15 rows for full detail + resume
points). Re-run `go test -v -run '^TestPort_PgDumpConnectionSetup$'
./internal/testport/` after to confirm the probe moves further (or fully
passes) once CREATE TYPE is fixed.

Gates run (all PASS this loop): go build ./...; go vet ./... (whole repo);
go test ./internal/parser/... ./internal/catalog/... ./internal/executor/...
./internal/wal/... ./internal/initdb/... (clean); go test -short full repo
excl. testport (52 packages, 0 FAIL, incl. internal/initdb 242s clean);
go test -v -run '^TestPort_PgDumpConnectionSetup$' ./internal/testport/ PASS
(probe moved to the gtype blocker, logged not failed); scripts/tpch-
spotcheck.sh PASS (Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke
ralph-precommit-test.sh PASS clean on first try (0 failed, 3 workloads);
make ralph-state-guard — auto-repaired 1 stale marker (previous loop's
clean-exit progress.json), consistent after.

In-flight: none — task complete; commit + pre-commit hook's own pgbench smoke
+ push are the only remaining mechanical step (about to run). Untouched
foreign/stray files present at loop start and still present (analysis/tpch-
explain-baseline.md, ci/logs/launch.log, postgres submodule dirty,
weekly_loc.*, analysis/perf-optimize3/runs/*, kaitai-struct-dash*.txt) — same
as every prior loop, left alone (not part of this loop's diff).
