Task: M0134-0067 (domain.sql) — sized (two dominant gaps both already-ledgered
architectural, cross-case: btree opclass generality; general assignment-target
indirection), PARKED (case still `failed`, CSV row unchanged). Landed a real
contained fix: `ALTER DOMAIN <name> ADD CONSTRAINT` now rejects with PG's exact
`0A000` "cannot alter type ... uses it" message when any table column
transitively contains the domain (composite field/array, domain-of-domain,
range subtype). Committed & pushed (07e3dfe3). Next: select M0134-0068
(drop_if_exists.sql).

Files this loop: `internal/catalog/catalog.go` (new
`FindColumnUsingDomainTransitively` + `typeUsesDomainLocked` recursive helper,
depth-16-capped, cycle-guarded, near `DropDomain` ~line 23530),
`internal/executor/operators_ddl.go` (`execAlterDomain`'s "addconstraint" case,
~line 24246, calls the new helper before `AddDomainConstraint`), new test
`internal/executor/alter_domain_add_constraint_dependency_test.go` (6
sub-tests: plain domain column + 5 transitive shapes + 1 non-dependent
regression guard), `.ralph/deferral_ledger.md` (new row, M0134-0067, full
bucket taxonomy), `.ralph/fix_plan.md` (M0134-0067 marked PARKED with summary,
still unchecked).

Key symbols: `catalog.InMemory.FindColumnUsingDomainTransitively` /
`typeUsesDomainLocked` (internal/catalog/catalog.go); `execAlterDomain`
(internal/executor/operators_ddl.go:24207).

Hypothesis/Findings: domain.sql's two dominant buckets are NOT new discoveries
— both are already-ledgered cross-case architectural gaps shared with prior
M0134 parks: (1) btree opclass hard-codes int4/numeric comparators (blocks any
UNIQUE/PK column of composite/array-of-composite type — same as
M0134-0047/-0060/-0064), and (2) general subscript/field indirection missing
from INSERT column-target/UPDATE SET-target grammar (extends M0134-0064 Bucket
A from SELECT-context `(expr).field` into assignment targets like `col[1]`,
`col[1].field`). Smaller untouched buckets, each independently viable as a
future contained brief: 4 missing information_schema views (domains,
domain_constraints, column_domain_usage, check_constraints — same pattern as
`registerInformationSchemaTables`), `pg_basetype(regtype)` builtin missing
(trivial), CHECK ENFORCED/NOT ENFORCED clause unparsed on domains (small,
shared grammar surface with table constraints), plpgsql domain-array typmod
handling (~70 diff lines, likely ties to known plpgsql text-substitution
weakness, not root-caused further).

Next step: select **M0134-0068 (drop_if_exists.sql)** per the fix_plan
task-ID-ascending selection rule — size via researcher first.

Gates run this loop: `go build ./...` PASS; `go test
./internal/catalog/... ./internal/executor/...` PASS (targeted, no
-count=1); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(full unit suite, internal/initdb cold at 442s, rest cache-warm); `make
ralph-state-guard` — found 2 inconsistencies (stale running/completed markers
from prior loop's clean-exit), auto-repaired, then PASS; pre-commit pgbench
smoke PASS (12122 TPS select-only, 659 TPS simple-update, 338 TPS TPC-B, 0
failed transactions across all three).

Delegation: researcher agent `a2308b4dda5dca637` (1 round — full bucket
breakdown across 7 buckets + PARK recommendation with Bucket-3
implement-and-park-anyway suggestion, accepted as-is); implementer agent
`aba03ddeecac11cae` (1 round — DONE, no escalation, resolved the brief's
flagged SQLSTATE ambiguity itself by tracing PG source to `0A000`, verified
zero regression, all targeted tests passing).

In-flight: none. Commit `07e3dfe3` pushed to `regress-renumbering`. No server
left running (pgbench smoke cluster stopped/cleaned by the pre-commit hook
itself).
