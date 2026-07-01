(idle — nothing in flight)

Loop #47 COMPLETE, committed, and PUSHED (3ddda6fc): CREATE/DROP TRANSFORM
restart persistence via WAL replay, closing the "restart persistence stays
in-memory only" deferral (tracked since slice 389) for TRANSFORM — the first
of the cast/conversion/transform/collation backlog to close.

What landed: RecordKindCreateTransform(36)/RecordKindDropTransform(37) WAL
kinds + Encode/Decode functions (internal/wal/recovery.go, mirrors
RecordKindCreateSchema/DropSchema M0110-0003 exactly — physical redo is a
no-op); RegisterTransformDuringRecovery/DropTransformDuringRecovery
idempotent catalog hooks (internal/catalog/catalog.go); new
internal/initdb/transform_ddl_recovery.go (replayTransformDDLRecords)
wired into open.go right after replaySchemaDDLRecords; executor WAL
emission at both CREATE "transform" and DROP "transform" arms
(internal/executor/operators_ddl.go).

A real bug was caught (not hypothetical): first EncodeCreateTransform cut
under-allocated the output buffer by 2 bytes (missed langLen field size),
caught immediately by TestEncodeDecodeCreateTransformRoundTrip.

Gates run: go build/vet clean; -race on internal/wal+internal/catalog (WAL
practice card); full wal/catalog/initdb/executor/parser suites PASS;
TestPort_PgDumpConnectionSetup (whole suite) PASS; TestE2E_PhysicalReplication
PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; live manual 3x-restart psql
end-to-end verification (build+initdb+start+kill+restart, twice) confirmed
pg_transform survives CREATE and DROP across restarts with correct OIDs.
pgbench smoke PASS at pre-commit. make ralph-state-guard: self-repaired the
same recurring stale status/progress marker as prior loops, OK after repair.

Design doc updated: docs/design/0110-0001-pg-dump-tap-port.md, new
"CREATE TRANSFORM restart persistence (WAL replay)" subsection. Ledger: new
row appended (slice-404 restart-persistence follow-up) documenting what
landed + the still-open cast/conversion/collation restart-persistence gap +
a concrete numbered resume-point template. fix_plan.md: loop #47 note
appended under M0119-0004.

Next loop candidates (pick ONE, same bounded pattern as this loop):
- Repeat this loop's exact template for CAST: RecordKindCreateCast(38)/
  DropCast(39), Encode/Decode sized to catalog.Cast{FromType,ToType,Context,
  Method,FuncOID} (multi-field, not just a name+lang pair like Transform —
  check the exact struct in internal/catalog/catalog.go before copying),
  *DuringRecovery hooks, internal/initdb/cast_ddl_recovery.go, wire into
  open.go, WAL-emit at operators_ddl.go's cast CREATE (~line 12995) and
  DROP (~line 12300) call sites.
- OR CONVERSION (catalog.UserConversion, bytes 40/41) — same shape,
  operators_ddl.go CREATE ~line 13076, DROP wherever "conversion" DROP
  lives.
- OR COLLATION (bytes 40/41 or 42/43 depending on order picked) — check
  internal/catalog/catalog.go for the exact collation registry struct
  first; it may differ in shape from the other three.
- Re-scan deferral_ledger.md for other open "| - |" rows if none of the
  above appeal (86+ open rows as of this loop).
- Whichever is picked: update fix_plan.md AND the design doc in the SAME
  loop (this loop did both — keep it up). Run the WAL/MVCC practice card
  gates (-race on wal+catalog, TestE2E_PhysicalReplication) for any of
  these — they all touch internal/wal/.
