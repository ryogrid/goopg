Loop #49 COMPLETE, committed and pushed (9479ea48): CREATE/DROP CONVERSION
restart persistence via WAL replay — third of the slice-389 "in-memory
only" backlog to close (TRANSFORM closed loop #47; CAST closed loop #48;
CONVERSION closed this loop; COLLATION remains, last item).

What landed: RecordKindCreateConversion(40)/RecordKindDropConversion(41)
WAL kinds + Encode/Decode functions (internal/wal/recovery.go);
CreateConversionDuringRecovery/DropConversionDuringRecovery idempotent
catalog hooks (internal/catalog/catalog.go — Drop is a thin wrapper since
DropConversion was already replay-safe); new
internal/initdb/conversion_ddl_recovery.go (replayConversionDDLRecords),
wired into open.go AFTER replaySchemaDDLRecords (conversions are
schema-scoped, unlike Cast/Transform — this ordering matters); executor
WAL emission at CREATE "conversion" and the DROP fallthrough's
"conversion" case (internal/executor/operators_ddl.go).

Two real bugs caught, not hypothetical:
1. EncodeCreateConversion under-allocated by 8 bytes (forgot the four
   2-byte length prefixes on top of the 22-byte fixed header) — third
   hand-derived byte-offset bug in a row across this three-loop series,
   caught by TestEncodeDecodeCreateConversionRoundTrip.
2. BIGGER FIND, cross-cutting, NOT conversion-specific: all four
   DDL-recovery scanners (schema/transform/cast/conversion) blindly
   switched on rec.Payload[0] with no check for rec.XLog != nil (a
   PG-native/canonical XLogRecord, e.g. Init()'s XLOG_CHECKPOINT_SHUTDOWN,
   whose MainData is raw PG struct bytes with NO goopg kind tag).
   The checkpoint's raw redo-LSN low byte coincidentally equalled 40,
   causing TestConversionDDLRecoveryReplaysCreate to fail on a bare
   Init+Open with zero conversions ever appended. Same collision class
   wal.ApplyRecord already guards (M0106-0011 comment), just never
   guarded in the catalog-only scanners — latent in schema/transform/cast
   too, just never triggered by their specific kind-byte values before.
   FIXED for all four via new exported wal.IsGoopgNativeRecord(r Record)
   bool (internal/wal/recovery.go, next to ApplyRecord): true iff
   r.XLog == nil OR (r.XLog.Header.Rmid == RmgrXLog && r.XLog.Header.Info
   == xlogInfoDefault/0xF0). Verified load-bearing (not defensive-only):
   a naive `rec.XLog != nil` skip breaks ALL FOUR ...ReplaysCreate tests,
   because the legitimate CREATE records are ALSO canonical-framed in
   this WAL mode — only the Rmid/Info check distinguishes goopg-native
   from real-PG-native. The sibling ...ReplaysDropAfterCreate tests
   stayed green even with the broken naive skip (a create-then-drop
   assertion can't tell "replay cancelled correctly" from "replay
   skipped everything") — the create-only positive test is what
   actually caught this, kept in the suite for that reason.

Gates run: go build/vet clean; -race -count=1 on internal/wal+catalog+
initdb PASS (228s on initdb, includes full recovery test suite);
TestE2E_PhysicalReplication + ...Sync PASS; TPC-H spotcheck Q12=2/Q13=33
PASS; full re-run of sibling TestSchemaDDLRecoveryReplaysCreate/
TestCastDDLRecoveryReplaysCreate/TestTransformDDLRecoveryReplaysCreate to
confirm the IsGoopgNativeRecord fix didn't regress them (all PASS).
pgbench smoke ran automatically via pre-commit hook on commit (PASS).
make ralph-state-guard: consistent, no repair needed this run.

Design doc updated: docs/design/0110-0001-pg-dump-tap-port.md, new
"CREATE CONVERSION restart persistence" subsection with full bug
narrative (both bugs). Ledger: new row appended (deferred: collation
registry restart persistence, bytes 42/43 next-free; DROP CAST
type-name-synonym gap from loop #48 still open, unrelated).
fix_plan.md: loop #49 note appended under M0119-0004.

Next loop candidate (bounded, same template, LAST item in this backlog):
- CREATE/DROP COLLATION restart persistence (catalog.UserCollation,
  internal/catalog/catalog.go ~8622 CreateCollation/DropCollation).
  Check the exact field set first — collation carries ICU/libc/builtin
  provider-specific fields (rules, deterministic flag, locale/lc_collate/
  lc_ctype per slices 391-394) that differ in shape from Cast/Transform/
  Conversion, so the WAL wire format needs its own field-mapping pass
  before pinning bytes 42/43. The wal.IsGoopgNativeRecord guard from this
  loop is already generalized — the new collation recovery driver just
  needs to use it from the start (copy conversion_ddl_recovery.go's
  guard line verbatim), no further WAL-scan investigation needed.
- After collation: the four-object "in-memory only" backlog is CLOSED;
  re-scan deferral_ledger.md for the next highest-value open "| - |" row
  (90+ open rows as of this loop) — e.g. the DROP CAST type-name-synonym
  key gap (internal/catalog/catalog.go RegisterCast/DropCast/CastByTypes)
  is a good independent candidate, unrelated to WAL work.
- Whichever is picked: update fix_plan.md AND the design doc in the SAME
  loop. Run the WAL/MVCC practice card gates (-race on wal+catalog+
  initdb, TestE2E_PhysicalReplication) for any WAL-touching option.
