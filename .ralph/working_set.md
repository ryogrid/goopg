Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 45 COMPLETE
(to be pushed). NEXT loop starts on slice 46 (unnest-join right-side projection
offset). NOTHING in flight after commit.

=== DONE (loop #68) — DU-002 slice 45 ===
Typed unnest elements now join catalog columns. pg_dump's getTableAttrs reads
columns via `FROM unnest('{oid}'::oid[]) AS src(tbloid) JOIN pg_attribute a
ON src.tbloid = a.attrelid`; the join matched NOTHING because expandArrayDatum
returns each element as a text KindString ("s:16403") whose datumKey differs
from the KindInt key an oid catalog column (pg_attribute.attrelid via
NewIntDatum) derives — hash join bucketed the sides apart.
Fix: new coerceUnnestElem in internal/executor/operators_from_unnest.go casts
each element to its declared output-schema type (oid/xid/int/float/numeric/bool
family only; text stays string; cast failure → raw element) in BOTH the
single-arg and multi-arg unnest paths.
Files: internal/executor/operators_from_unnest.go (coerceUnnestElem +
unnestElemNeedsTyping + call sites), internal/executor/operators_from_unnest_test.go
(NEW: TestCoerceUnnestElem_JoinKeyParity / _TextUnchanged / _NullPassthrough /
_BadValueFallsBack), docs/design/0110-0001-pg-dump-tap-port.md (slice 45+46),
internal/testport/pgdump_connsetup_test.go (header updated for slice 45/46).
Gates: gofmt/build clean; executor pkg PASS (1.46s); TestPort_PgDumpConnectionSetup
PASS. tpch-spotcheck SKIPPED (no data dir); pgbench CI-parity runs in pre-commit.

=== NEXT STEP — DU-002 slice 46 (unnest-join projection offset) ===
With the join key fixed, pg_dump now fails `invalid column numbering in table
"foo"` (exit 1). DIAGNOSED: the join CONDITION resolves a.attrelid to combined
idx 1 correctly, but the PROJECTION of right-side columns is NOT shifted by the
1-column unnest (left) prefix. Combined join row =
[src.tbloid(0), a.attrelid(1), a.attname(2), a.atttypid(3), a.attlen(4), a.attnum(5)...].
Empirically a.attname → combined idx 1 (returns attrelid=16403), a.attnum →
combined idx 4 (returns attlen=4 for integer). A DIRECT pg_attribute scan
returns the correct 1=id/2=name, so the bug is isolated to right-side
column-ref resolution in a join whose left input is FromUnnest (sibling-path
disagreement: join-condition resolution shifts, projection resolution does not).
Look at planner shiftColumnRefsBy / how FromUnnest's schema width is registered
when it is a join's left input. To reproduce: CREATE TABLE foo(id int,name text),
then `SELECT a.attrelid,a.attnum,a.attname FROM unnest('{16403}'::oid[]) src(tbloid)
JOIN pg_attribute a ON src.tbloid=a.attrelid WHERE a.attnum>0` — returns one
scrambled row instead of two correct rows.

ORTHOGONAL PRE-EXISTING (track separately): plpgsql user functions can't be
dumped (plpgsql not in pg_language → prolang=0 → dumpFunc join 0 rows).
Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
