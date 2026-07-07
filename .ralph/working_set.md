Task: M0122-0006 follow-up 2 of 2 — "checkpointed-restart heap decode" of
index properties beyond ordering (predicate/INCLUDE columns/NULLS NOT
DISTINCT survive a *graceful* restart, not just the uncheckpointed-crash
WAL path fixed last loop). COMPLETE and committed this loop.

Files: internal/executor/pg18_user_catalog_rows.go (buildUserPGIndexRow:
computes real nkeyatts=len(idx.Columns)/natts=nkeyatts+len(IncludeColumns)
instead of always nkeyatts==natts; appends IncludeColumns attnums to
indkey after key-column attnums; writes indpred as idx.PredicateString
when idx.HasPredicate, was always NullDatum before), internal/catalog/
codec.go (PGIndexRow gained IndNKeyAtts/IndNullsNotDistinct/IndHasPred/
IndPred; DecodePGIndexPhysicalRow now reads indnullsnotdistinct from the
previously-unread offset-13 byte and decodes indpred via new
decodePGIndexVarlenaText helper — NULL-vs-present inferred from data
length alone since indexprs, the column just before indpred, is proven
always NULL [goopg has no expression-index support] and NULL columns
consume zero bytes in encodeRowPG's physical tuple format), internal/
initdb/open.go (loadUserIndexesFromHeap Pass 2/3: recoveredIndex struct
gained indNKeyAtts/nullsNotDistinct/hasPred/predText; indkey is now split
at indnkeyatts into key-column names vs IncludeColumns instead of
treating every attnum as a key column; RegisterIndexDuringRecovery call
now passes real hasPredicate/predicateString/includeColNames/
nullsNotDistinct instead of the old `false, "", nil, ..., false` zero
literal — ColOpClasses/ColCollations args still pass nil, that piece
remains open), internal/initdb/index_ddl_recovery_test.go (new
TestCreateIndexPredicateAndIncludeColumnsSurviveCheckpointedRestart, uses
a plain rt1.Close() — checkpointed restart — unlike the existing
crash-only tests), docs/design/0122-0006-index-column-order-restart-
persistence.md (new "Follow-up 2 (2026-07-08)" section), docs/design/
README.md (0122-0006 row extended), .ralph/deferral_ledger.md (prior
2026-07-08 "index properties beyond ordering" row flipped to resolved;
new row appended narrowing the remaining opclass/collation OID gap),
.ralph/fix_plan.md (M0122-0006 bullet dated follow-up-2 update).

Key symbols: buildUserPGIndexRow (pg18_user_catalog_rows.go) — nKey :=
len(idx.Columns), nInclude := len(idx.IncludeColumns), colAttnum(name)
helper closure, indkey built as key-attnums-then-include-attnums.
DecodePGIndexPhysicalRow (catalog/codec.go) — after decoding indoption,
predOff := pgIndexAlign4(off + vectorHdrSize + 2*m); if predOff <
len(data) then decodePGIndexVarlenaText(data[predOff:]) sets
IndHasPred/IndPred. decodePGIndexVarlenaText — local reimplementation of
executor/codec.go's varlenaTextBytes decode (short 1-byte header + long
4-byte header forms), kept local to avoid catalog→executor import cycle.
loadUserIndexesFromHeap Pass 3 (open.go) — nKeyAtts :=
int(pgIdx.indNKeyAtts) (clamped to len(indKey) as a defensive fallback);
pgIdx.indKey[:nKeyAtts] → colNames/colDescending/colNullsFirst,
pgIdx.indKey[nKeyAtts:] → includeColNames.

Findings: The real observable target for this whole M0122-0006 cluster is
pg_get_indexdef/\d fidelity, which reads directly from catalog.Index's
name-string fields (PredicateString/IncludeColumns/ColOpClasses/
ColCollations), NOT from pg_index's numeric OID columns — so the
heap-recovery gap that actually mattered was "does RegisterIndexDuringRecovery
get real values on the heap path", not "does pg_index.indclass/indcollation
contain correct OIDs". Discovered along the way: pg_index's LIVE (no
restart) VirtualRows builder (catalog.go ~line 7660) ALSO never uses
idx.ColOpClasses/ColCollations for indclass/indcollation — it renders
indclass via a hardcoded per-Go-type-name default-opclass switch and
indcollation as always-zero, regardless of restart. So real opclass/
collation OID accuracy in pg_index is a separate, pre-existing,
materially larger gap (needs a full builtin-opclass/collation
name↔OID registry, not just heap-decode plumbing) — confirmed NOT
something this loop needed to touch for pg_get_indexdef parity, and
explicitly scoped out (new ledger row) rather than half-attempted.
Confirmed non-vacuous via git stash on the 3 impl files: new test's 4
assertions (HasPredicate/PredicateString/IncludeColumns/NullsNotDistinct)
all fail with exact pre-fix zero-value symptom without the fix. Live
end-to-end verified against the real cmd/goopg binary with a GRACEFUL
stop/start (not kill -9): `CREATE UNIQUE INDEX ext3_idx ON ext3 (a, b)
INCLUDE (c) NULLS NOT DISTINCT WHERE (a > 0)` — pg_get_indexdef and
pg_index.indnatts/indnkeyatts/indkey/indnullsnotdistinct all correct
post-restart.

Next step: pick the next task. M-NIGHTLY is clean (run 20260707-000712,
all 8 items tracked/resolved in fix_plan.md; ci/logs/action-items.md
unchanged since). Candidates: (a) the newly-scoped opclass/collation OID
resolution gap above — build a builtin opclass-name→OID + collation-
name→OID registry (covering the full standard-type universe, not just
builtinRangeSubtypeOpclasses' small range-type subset) plus reverse
OID→name, wire into BOTH the live pg_index VirtualRows builder
(catalog.go ~7660) and buildUserPGIndexRow/DecodePGIndexPhysicalRow —
sizeable, may warrant its own design doc and possibly >1 loop; (b)
M0122-0006's remaining named items (pg_tablespace visibility, fuller
pg_index heap persistence framing); (c) M0122-0007/0008 quick wins
(DDL/admin commands, auth/roles — SASLprep/channel binding/
scram_iterations still open in 0122-0008); (d) resume M0119-0004/0005/
0006/0007 per the Current Priority banner — M0119-0004's per-database
catalog-isolation gap (TestPort_PgDumpConnectionSetup's "collation
builtin_coll already exists" DU-002 residual), M0119-0006's
005_opclass_damage.pl two-part gap (pg_amproc Virtual-UPDATE path +
internal/access/btree has zero opclass/comparator dispatch — NOTE: likely
overlaps with candidate (a)'s opclass registry work, check for shared
scope before picking both), M0119-0005's hash/gin/gist/spgist/brin index
AM gap for 001_basic.pl's server-dependent tier.

Gates run: go build ./... clean, go vet ./internal/catalog/...
./internal/executor/... ./internal/initdb/... clean. go test
./internal/catalog/... ./internal/executor/... ./internal/initdb/...
./internal/wal/... ./internal/planner/... ./internal/analyzer/...
./internal/parser/... ./internal/server/... PASS. scripts/
tpch-spotcheck.sh PASS (Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads). Live
e2e: real cmd/goopg binary, graceful stop/start restart, verified
pg_get_indexdef + pg_index (indnatts/indnkeyatts/indkey/
indnullsnotdistinct) all correct post-restart. make ralph-state-guard: 2
benign issues auto-repaired (identical pattern to every prior loop —
status/progress running-vs-completed reconciliation).

In-flight: none. All manual verification servers/processes/temp files
killed and cleaned up (/tmp/goopg-verify*).
