# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 10 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 10 — NOT VALID** (commit `32fd11c5`).

**Status:** C2 slice 10 COMPLETE + committed. C2 remains OPEN.

**Findings:** Two `NOT VALID` sites, both byte-matching PG. (a) CREATE TABLE
table-level CHECK — both `parseTableConstraintElement` arms (anonymous + named)
consume-and-drop a trailing `NOT VALID` after `NO INHERIT`; PG auto-validates at
CREATE TABLE (parse_utilcmd.c:2946 + heap.c:2584-2587), so no convalidated='f'.
(b) ALTER ADD [CONSTRAINT] NOT NULL — the arm consumes `NOT VALID`
order-independently (before OR after NO INHERIT) onto `AlterTableAction.NotValid`,
threaded through `AddNotNull`'s new `notValid` param → `NamedNotNullConstraint.NotValid`
→ pg_constraint contype='n' convalidated `row[6]='f'`, flipped back to 't' by a
new `NotNullConstraints` loop in the VALIDATE CONSTRAINT arm (PG excludes
CONSTR_NOTNULL from the Phase-3 pre-scan, tablecmds.c:9956). `nv_parent`
(diff:534) + `atnnparted` (diff:1075) → 0 (32→30 syntax-error lines).

**Files:** internal/parser/{ddl.go,alter_test.go,check_alter_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,
operators_ddl_named_check_test.go},
docs/design/0134-0002-alter-table-sql-divergence.md (slice-10 entry),
.ralph/fix_plan.md, .ralph/deferral_ledger.md (1 row: order-dependent CREATE
TABLE trailer loop).

**Key symbols:** `parseTableConstraintElement` CHECK arms (ddl.go ~3749/3921
consume-and-drop); ALTER ADD NOT NULL arm (ddl.go ~9744-9756, order-independent
NOT VALID + second NO INHERIT); `AddNotNull` + `NamedNotNullConstraint.NotValid`
(catalog.go:242/253); pg_constraint `PGConstraintRowsForDBOid` contype='n'
row[6] (catalog.go:6697); VALIDATE CONSTRAINT `NotNullConstraints` loop
(operators_ddl.go:7774).

**Remaining C2 sub-gaps (ranked):** ANALYZE tab(col) (4 — re-route: an
ANALYZE/VACUUM statement gap, NOT ALTER TABLE), OF/NOT OF (3, typed-table arms
absent in parseAlterTableAction), SET WITHOUT OIDS (1), ENFORCED dup (1,
C9-masked).

**Next step:** C2 slice 11 — **OF/NOT OF** (3 sites). NO research exists yet;
needs a `researcher` round first to map `ALTER TABLE … OF type_name` /
`… NOT OF` (AT_OfType/AT_NotOf) in `parseAlterTableAction` + the PG semantics
(gram.y typed-table forms; whether goopg models typed tables at all, or the arm
should raise a bounded "not supported" error matching PG's test expectations).

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/
./internal/executor/ ./internal/catalog/` PASS (3 new named tests);
`scripts/pg-regress-runner.sh alter_table` — the 2 target lines → 0 (32→30);
pre-commit pgbench smoke PASS (11956 tps select-only). M-NIGHTLY 20260816
triage: 2 items stale at HEAD (build clean; TestPort_FunctionSurvivesRestart
PASS 5.8s) — marked checked; `race/internal/initdb` left open (infra
time-budget, not a code bug).

**Delegation:** tester `0134-nightly-triage-20260816` DONE (both repros clean);
implementer `0134-0002-c2-slice10-notvalid` DONE (1 round, report inline — env
blocked report.md writes).

**In-flight:** none.
