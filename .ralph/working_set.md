(idle — nothing in flight)

Last landed: DU-002 slice 119 (loop #83) — descending sequences (`INCREMENT BY < 0`)
now verified to dump byte-identically. VERIFICATION slice, no production code changed:
pg_dump's dumpSequence flips default_minv/default_maxv by increment sign (descending
bigint → minv=PG_INT64_MIN, maxv=-1, seqstart=seqmax); execCreateSequence already
computes these (seqTypeBounds min, maxV=-1, start=maxV) + threads min/max/start via
pg_sequence; SequenceRowData returns start (not current=start-increment) when uncalled
→ setval last_value=-1. Plain `INCREMENT BY -1` → START WITH -1 / NO MINVALUE / NO
MAXVALUE; explicit-bound `INCREMENT BY -2 MINVALUE -100 MAXVALUE -5` → both bounds +
START WITH -5. Verified byte-identical vs real pg_dump 18.3 (/tmp/pgcheck_du119).
Files: internal/testport/pgdump_connsetup_test.go (desc_seq + desc_bound_seq fixtures +
assertions), docs/design/0110-0001-pg-dump-tap-port.md (Slice 119 section).

The SINGLE-sequence pg_dump surface is now EXHAUSTED: plain, explicit-bound, typed
(AS smallint/integer), CYCLE, OWNED BY, descending all covered.

Next direction (slice 120): PIVOT to a multi-statement / multi-object pg_dump surface.
Candidates: (a) identity column's owned sequence `GENERATED … AS IDENTITY` — exercises
pg_dump's deptype='i' path (is_identity_sequence=true → `ALTER TABLE … ADD GENERATED …
AS IDENTITY (SEQUENCE NAME …)` instead of CREATE SEQUENCE); (b) a table+sequence+view
dependency-ordering case. Identity is the natural next step — it reuses the slice-118
pg_depend machinery (deptype='i' instead of 'a') and is a distinct pg_dump code path.
