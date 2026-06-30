(idle — nothing in flight)

Loop #24 COMPLETE: M0119-0004 DU-002 slice 385 — multiple CHECK constraints on a
CREATE DOMAIN. PG stores each CHECK as a separate pg_constraint row (contype='c',
contypid=domain OID) emitted inline by pg_dump ORDER BY conname. goopg modelled
only ONE check; the parser silently dropped every CHECK after the first.

Refactor (single-check scalar → slice):
- parser/ast.go: CreateDomainStmt.{CheckExpr,CheckName,CheckInValues} → Checks
  []DomainCheckClause{Name,Expr,InValues}; new type.
- parser/ddl.go (parseCreateDomainTail): both CHECK arms append a clause instead
  of keeping only the first.
- catalog/catalog.go: Domain.{CheckExpr,CheckName,CheckOID,CheckInValues} →
  Checks []DomainCheck{Name,Expr,OID,InValues}; new DomainCheck type. SetDomainCheck
  → AddDomainCheck (append + per-check OID + ChooseConstraintName disambiguation
  `<domain>_check`/`_check1`/… via domainCheckNameTaken). RegisterDomain dropped
  unused checkInValues variadic. pg_constraint row loop iterates d.Checks.
- executor/expr.go: pg_get_constraintdef + cast-time IN-values enforcement iterate
  d.Checks (per-check OID match / per-check membership; error uses ck.Name).
- executor/operators_ddl.go (execCreateDomain): loops s.Checks.

Files: internal/parser/ast.go, internal/parser/ddl.go, internal/catalog/catalog.go,
internal/catalog/domain_check_test.go (NEW TestAddDomainCheckNaming),
internal/executor/expr.go, internal/executor/operators_ddl.go,
internal/testport/pgdump_connsetup_test.go (multichk/mixchk fixtures + dom cols +
domainDefs asserts), docs/design/0110-0001-pg-dump-tap-port.md (Slice 385),
.ralph/fix_plan.md + .ralph/deferral_ledger.md.

Gates: catalog+parser+executor unit suites PASS; TestPort_PgDumpConnectionSetup
PASS (5.4s, byte-identical vs pg_dump 18.3); go build ./... clean; go vet clean;
gofmt clean on my edits (ast.go's other hunks are pre-existing go1.25/1.26 noise).
pgbench smoke = pre-commit hook. No TPC-H (catalog/render-only, no row path).

Deferred (ledger): runtime enforcement of GENERIC (non-IN) domain CHECK predicates
— dumped/round-tripped but not evaluated on cast (pre-existing, now spans all checks).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: domain NOT NULL as
contype='n' pg_constraint row (PG 17+); CREATE COLLATION; range types; aggregates;
operators; text-search configs.
