Task: M0119-0006 slice — operator-class comparator dispatch in amcheck's B-tree
verifier (the read-side unlock for pg_amcheck 005_opclass_damage.pl)

Files:
- internal/amcheck/verify_nbtree.go: KeyComparator type + VerifyBtreeItemOrderCmp;
  VerifyBtreeItemOrder now delegates with nil (= btree.CompareKeys)
- internal/catalog/catalog.go: LookupOpClassSupportProcOID (PG get_opfamily_proc),
  reads the LIVE amProcMembers store so runtime UPDATE pg_amproc is observed
- internal/executor/operators_bt_index_check.go: btIndexOpClassComparator +
  comparator threaded through btIndexVerify
- internal/executor/operators_bt_index_check_test.go: TestBtIndexCheck_OpClassDamageDetected
- docs/design/0119-0006-opclass-comparator-dispatch-amcheck.md (accepted) + README row
- .ralph/deferral_ledger.md, .ralph/fix_plan.md

Key symbols:
- amcheck.KeyComparator / VerifyBtreeItemOrderCmp
- catalog.InMemory.LookupOpClassSupportProcOID
- executor.btIndexOpClassComparator (btree.DecodeInt4 + executeStoredRoutine)

Hypothesis/Findings:
- Upstream amcheck never compares keys itself — every comparison goes through
  the opclass's FUNCTION 1 (BTORDER_PROC). That indirection IS what 005 tests.
- A nil comparator is the FAITHFUL answer for a built-in opclass: goopg's key
  encoding IS that class's order; only user classes name a real function.
- Non-vacuity confirmed: with the comparator forced nil the damaged index
  reports clean and the new test fails.

Next step:
(1) M0119-0006 continued: general encoded-key→Datum decoder (inverse of
    encodeBTreeKeyForColumn) to lift the single-int4-column restriction, then
    the --checkunique tier, then port 005_opclass_damage.pl to internal/testport.
(2) Otherwise re-read the Current Priority banner: M0130 is all [x]; remaining
    unchecked M0119 items are 0005 (pg_waldump server tier, needs index AMs)
    and 0007 (blocked on logical decoding), then M0122.

Gates run:
- go build ./...: CLEAN
- go test ./internal/amcheck/... ./internal/catalog/...: PASS
- go test -run TestBtIndexCheck_ ./internal/executor/: PASS (new test non-vacuous)
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- scripts/tpch-spotcheck.sh: PASS (Q12=2, Q13=35)
- make ralph-state-guard: CONSISTENT
- commit hook pgbench smoke: see commit

In-flight: none
