Task: M0119-0004 DU-002 — per-AM `amadjustmembers` dependency-strength policy
for GiST/SP-GiST (closes the loop #40 ledger row, flagged in loop #44's carry
as the largest remaining structural gap). COMPLETE and committed this loop
(#45).

Files: internal/catalog/catalog.go (AmProcMember.Method field,
RegisterAmProcMember signature, amGISTMethodOID/amSPGistMethodOID consts,
gistRequiredSupportProcs/spgistRequiredSupportProcs, amForcesSoft{Operator,
Function}Dependency, dependVirtualRows's two member loops);
internal/executor/operators_ddl.go (RegisterAmProcMember call site threads
methodOID); internal/executor/create_operator_test.go (corrected
TestCreateOperatorClassForOrderBySortFamily's stale "n"→"a" assertion, new
TestCreateOperatorClassGistMembersGetSoftDependencies);
docs/design/0119-0004-create-operator-roundtrip.md +
docs/design/README.md (Loop #45 addendum); .ralph/fix_plan.md +
.ralph/deferral_ledger.md (new row).

Key symbols: amForcesSoftOperatorDependency/amForcesSoftFunctionDependency,
dependVirtualRows (catalog.go), registerOpClassMembers (operators_ddl.go).

Findings: real PG's DefineOpClass/AlterOpFamilyAdd (opclasscmds.c) call
amRoutine->amadjustmembers unconditionally before storeOperators/
storeProcedures; gist/spgist's override forces every OPERATOR member soft
(family-level) and every non-required FUNCTION member soft too, REGARDLESS
of class-attribution — goopg previously only branched on ClassOID==0. This
also flipped a stale test assertion (loop #40's FOR-ORDER-BY test asserted
NORMAL where AUTO was always correct). Verified end-to-end against two live
side-by-side servers (goopg port 5533 vs a fresh real PG 18.3 initdb on port
5534, same pg_dump binary): the OPERATOR/optional-FUNCTION members now
round-trip via the existing ALTER OPERATOR FAMILY ADD path with zero new
dump-side code, byte-identical to real PG (only cross-object dump *ordering*
differs — ledgered as a separate, larger, unrelated gap).

Next step (idle — nothing in flight for THIS task). Pick the next M0119-0004
item per the design doc's "Still open" list:
(1) Dump object-ordering / topological sort (goopg's dump path has no
    dependency-graph ordering pass at all — materially larger, own
    milestone-sized scope, not a small slice).
(2) btree/hash's own `amadjustmembers` cross-type-driven rule
    (nbtvalidate.c/hashvalidate.c) — only worth doing if a future fixture
    needs a cross-type btree/hash opclass member; none does today.
(3) Builtin-operator catalog: extend only when a new fixture forces a
    different builtin operator (don't do speculatively).
(4) M0119-0009 (UPDATE/DELETE conflict-wait on a conflicting lock-only
    locker) remains a well-scoped, independently resumable alternative if
    none of the above look tractable — needs a purpose-built isolation
    fixture plus the full row-lock/multixact/-race + pgbench gate suite.

Gates run this loop: go build ./... clean; go vet (catalog+executor) clean;
targeted TestCreateOperatorClass*/TestAlterOperatorFamily* PASS; full
internal/catalog+internal/executor+internal/parser+internal/planner+
internal/server suites PASS; TestPort_PgDumpConnectionSetup PASS; live PG
18.3 end-to-end diff (byte-identical); TPC-H spotcheck Q12=2/Q13=33 PASS;
gofmt -l pre-existing-drift-only (verified via git stash); make
ralph-state-guard OK (self-repaired the usual stale timestamp marker).
pgbench smoke runs at commit time via the pre-commit hook.
