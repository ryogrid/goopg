(idle — nothing in flight)

Loop #43 landed and committed clean:
M0119-0004 DU-002 slice 416 — `ALTER OPERATOR FAMILY name USING method DROP
entry [, entry ...]`. Closes the loop #41 ledger row's resume point (1),
direct sibling of loop #41's ADD form.

What landed:
- Parser: `AlterOpFamilyDropStmt` (internal/parser/ast.go) +
  `parseAlterOpFamilyDropTail` (internal/parser/ddl.go), reached from
  `parseAlterOpFamilyTail` when the tail keyword is DROP rather than ADD.
  Models PG's narrower `opclass_drop` grammar: mandatory strategy/support
  number + mandatory parenthesized type list, no operator/function name
  (single-type shorthand defaults righttype=lefttype, matching
  processTypesSpec). Reuses OpClassMember.
- Executor: `execAlterOpFamilyDrop` (internal/executor/operators_ddl.go)
  resolves the family (42704 if missing) then removes each matching
  pg_amop/pg_amproc row via new `catalog.RemoveAmOpMember`/
  `RemoveAmProcMember` (internal/catalog/catalog.go), keyed
  (familyOID, leftType, rightType, strategy-or-procnum) — mirrors PG's
  GetSysCacheOid4(AMOPSTRATEGY/AMPROCNUM) lookup. Missing member -> 42704.
- No new pg_depend plumbing needed: dependVirtualRows (loop #41) already
  recomputes pg_amop/pg_amproc dependency rows live from
  c.amOpMembers/c.amProcMembers, so removing a catalog row auto-removes its
  dependency rows.
- Wired into server/dispatch.go (command tag) and internal/planner/planner.go
  (DDL passthrough case list) alongside AlterOpFamilyAddStmt.
- Verified against a freshly-built, live PG 18.3 instance
  (postgres/local_install, started manually on /tmp/ruletest_pgdata port
  5540): DROP removes exactly the targeted rows; repeat-DROP raises
  identically-shaped 42704; single-type shorthand matches the (t,t) row.
  Stopped the server after verification.
- Tests: TestParseAlterOperatorFamilyDrop/…DropRequiresParens (parser,
  replacing the stale loop #41 …DropStillNoop no-op pin);
  TestAlterOperatorFamilyDropRemovesLooseMember/…DropMissingMemberErrors/
  …DropUnknownFamilyErrors (executor).
- Gates all green: build/vet clean; parser+executor+catalog+planner+server
  suites PASS; TestPort_PgDumpConnectionSetup PASS (DROP has no pg_dump
  fixture of its own — pg_dump never emits this form); TPC-H spotcheck
  Q12=2/Q13=33 PASS; gofmt drift confirmed pre-existing via git stash
  (same 3 files as every prior loop in this chain).
- Design doc updated (docs/design/0119-0004-create-operator-roundtrip.md,
  "Loop #43" section) + README.md index entry appended (inside the
  giant accumulating 0119-0004bk row) + deferral ledger row appended.

Deferred (ledgered, NOT fixed — no exercising fixture): dropping a
CLASS-attributed (hard, ClassOID != 0) member removes the row
unconditionally instead of mirroring PG's performDeletion INTERNAL-
dependency cascade/restrict onto the owning opclass. Low priority — pg_dump
never issues this DDL form at all.

Next candidates (backlog, per the deferral ledger's open rows):
(1) Per-AM amadjustmembers dependency-strength policy (gist/spgist soft
deps for CLASS-attributed members) — needed for any real GiST/SP-GiST
opclass to round-trip through pg_dump; unblocks the op_class_custom
ordering fixture (range-type subtype_opclass binding). Larger/dedicated
scope, flagged repeatedly across loops #40/#41.
(2) Extend the builtin-operator catalog incrementally as new fixtures need
different builtin operators (still just the loop #39 6-row int8 slice).
(3) CREATE OPERATOR FAMILY's simple-query command tag renders bare CREATE
instead of CREATE OPERATOR FAMILY (cosmetic, ledgered loop #41).
(4) M0119-0005/0006/0007 (pg_waldump/pg_amcheck/pg_basebackup server
tiers). (5) M0119-0002 (CLOG store swap Part B) — flagged highest blast
radius, needs dedicated full-gate session.

Recommendation for next loop: (2) is the smallest/most mechanical filler if
no fixture forces (1); (1) is the largest structural gap in this design-doc
chain and deserves a dedicated session (thread an amoid-keyed policy table
into registerOpClassMembers/dependVirtualRows).
