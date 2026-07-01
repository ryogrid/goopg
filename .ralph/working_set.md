(idle — nothing in flight)

Loop #48 COMPLETE, committed (pending push): CREATE/DROP CAST restart
persistence via WAL replay — second of the slice-389 "in-memory only"
backlog to close (TRANSFORM closed loop #47; CAST closed this loop;
CONVERSION/COLLATION remain).

What landed: RecordKindCreateCast(38)/RecordKindDropCast(39) WAL kinds +
Encode/Decode functions (internal/wal/recovery.go, mirrors
RecordKindCreateTransform/DropTransform M0119-0004 exactly — physical redo
is a no-op); RegisterCastDuringRecovery/DropCastDuringRecovery idempotent
catalog hooks (internal/catalog/catalog.go); new
internal/initdb/cast_ddl_recovery.go (replayCastDDLRecords) wired into
open.go right after replayTransformDDLRecords; executor WAL emission at
both CREATE "cast" and DROP "cast" arms (internal/executor/operators_ddl.go)
— captures the *catalog.Cast returned by RegisterCast to emit the record.

A real bug was caught (not hypothetical): first EncodeCreateCast cut
under-allocated the output buffer by 1 byte (14 instead of 15 — fixed
header is kind(1)+oid(4)+funcOID(4)+context(1)+method(1)+sourceLen(2) = 13,
plus targetLen(2) = 15 total before the two names), caught immediately by
TestEncodeDecodeCreateCastRoundTrip. Same failure class as the TRANSFORM
loop's 2-byte miss — recurring risk of hand-derived byte-offset math.

Gates run: go build/vet clean; -race -count=1 on internal/wal+catalog+
initdb+executor; TestE2E_PhysicalReplication PASS; TPC-H spotcheck
Q12=2/Q13=33 PASS; live manual restart verification on isolated port 5533
(3x restart cycle: create 3 casts incl. WITH FUNCTION/WITH INOUT/WITHOUT
FUNCTION forms, confirm survive restart, DROP CAST, confirm drop survives
restart). pgbench smoke runs automatically via pre-commit hook on commit.
make ralph-state-guard: self-repaired the same recurring stale
status/progress marker as prior loops (progress.json "completed" from a
prior loop's clean exit, not project completion), OK after repair.

Design doc updated: docs/design/0110-0001-pg-dump-tap-port.md, new
"CREATE CAST restart persistence" subsection. Ledger: new row appended
documenting what landed + a genuine PRE-EXISTING (not introduced this loop)
DROP CAST type-name-synonym gap discovered during manual verification
(DROP CAST (real AS text) fails to find a cast created as
CREATE CAST (float4 AS text) — DropCast's key is raw lowercased parsed
spelling, not canonical type name; real/float4 don't cross-resolve) +
the still-open CONVERSION/COLLATION restart-persistence gap with a
concrete resume-point template. fix_plan.md: loop #48 note appended under
M0119-0004.

Next loop candidates (pick ONE, same bounded pattern as this loop):
- Repeat this loop's exact template for CONVERSION: RecordKindCreateConversion
  (40)/DropConversion(41), Encode/Decode sized to catalog.UserConversion
  (check exact field set in internal/catalog/catalog.go before copying —
  it carries ConvName/ForEncoding/ToEncoding/FuncOID/Default, encoding-alias
  strings not a type-name pair), *DuringRecovery hooks,
  internal/initdb/conversion_ddl_recovery.go, wire into open.go, WAL-emit
  at operators_ddl.go's conversion CREATE (~line 13076 area, search
  `case "conversion"`) and DROP call sites.
- OR COLLATION (bytes 40/41 or 42/43 depending on order picked) — check
  internal/catalog/catalog.go for the exact collation registry struct
  first; it may differ in shape from the other three (ICU/libc/builtin
  provider fields per slices 391-394).
- OR fix the DROP CAST type-name-synonym key gap discovered this loop
  (internal/catalog/catalog.go RegisterCast ~10154/DropCast ~10178/
  CastByTypes ~10212) — canonicalize the key through whatever builtin-type-
  alias resolution catalog.TypeNameToOID uses internally, or key by
  resolved OID instead of lowercased string. Independent of WAL work,
  its own bounded loop.
- Re-scan deferral_ledger.md for other open "| - |" rows if none of the
  above appeal (87+ open rows as of this loop).
- Whichever is picked: update fix_plan.md AND the design doc in the SAME
  loop (this loop did both — keep it up). Run the WAL/MVCC practice card
  gates (-race on wal+catalog, TestE2E_PhysicalReplication) for any of
  the WAL-touching options.
