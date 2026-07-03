Task: M0119-0004/M0110-0001 (DU-002) loop #65 — CREATE/DROP OPERATOR
restart/WAL persistence. Discovered while verifying loop #64's CREATE TYPE
... AS RANGE opclass/collation follow-up (a range type's user opclass
reference vanished on restart); root cause traced to CREATE OPERATOR itself
having zero WAL persistence.

Files:
- internal/wal/recovery.go — RecordKindCreateOperator(83)/RecordKindDropOperator(84)
  + CreateOperatorPayload/EncodeCreateOperator/DecodeCreateOperator/EncodeDropOperator/DecodeDropOperator
- internal/wal/operator_ddl_test.go (new) — encode/decode round-trip tests
- internal/catalog/catalog.go — RegisterUserOperatorDuringRecovery, DropUserOperatorByOIDDuringRecovery
- internal/initdb/operator_ddl_recovery.go (new) — replayOperatorDDLRecords
- internal/initdb/operator_ddl_recovery_test.go (new)
- internal/initdb/open.go — wired replayOperatorDDLRecords after range-type replay
- internal/executor/operators_ddl.go — WAL append at end of execCompatNoop's
  "operator" case (CREATE) and execDropCompat's "operator" case (DROP, by OID)
- docs/design/0119-0004-create-operator-roundtrip.md — "Loop #65" section appended
- docs/design/README.md — new row 0119-0004cl
- .ralph/deferral_ledger.md — new row (loop #65)

Status: COMPLETE and committed this loop (assuming tpch-spotcheck/pgbench
gates below pass — check git log if resuming).

Gates run: go build/vet clean; internal/wal+catalog+initdb+executor suites
PASS; TestPort_PgDumpConnectionSetup PASS; live goopg/psql/pg_dump smoke incl.
2 full restarts (CREATE OPERATOR survives, DROP OPERATOR stays dropped);
scripts/tpch-spotcheck.sh — check output if resuming (was running in
background when this file was last written); pre-commit hook (pgbench smoke)
runs automatically on `git commit`.

Next step (if this loop got cut off before commit): re-run
`scripts/tpch-spotcheck.sh`, then `git add` the listed files (NOT
.ralph/fix_plan.md — driver-churned, do not edit) + .ralph/progress.json,
commit, run `make ralph-state-guard`.

Still open (recorded in ledger, NOT this loop's scope):
1. CREATE OPERATOR CLASS/CREATE OPERATOR FAMILY/pg_amop/pg_amproc member
   persistence — userOperatorClasses/userOperatorFamilies + amop/amproc
   stores are still pure in-memory, zero WAL. This is the natural next
   follow-up loop (mirror this loop's exact pattern: RecordKind + Encode/
   Decode + *DuringRecovery + recovery driver + open.go wiring), sized
   larger (family FK + member lists) — its own loop.
2. Range-type canonical/subtype_diff (sub-item a remainder) + generic
   GetDefaultOpClass-over-user-opclasses (sub-item b) — unchanged from
   loop #63/#64.
3. New unrelated discovery: DROP OPERATOR public.=#= (int4, int4) fails
   with spurious 42883 (CREATE/pg_dump of the same symbol works fine) —
   a DROP-OPERATOR-specific lexer/parser quirk for "=...=" shaped operator
   symbols. Resume: internal/parser/lexer.go operator-char class vs
   whatever tokenizes DROP OPERATOR's name argument.
