(idle — nothing in flight)

Last landed: DU-002 slice 133 (loop #98) — cross-table FOREIGN-KEY
dependency-ordering (post-data split) regression guard for pg_dump. NO
production change: empirically verified vs goopg's own pg_dump output that the
FK from public.baz → public.bar is SPLIT out of the table body into a separate
post-data `ALTER TABLE ... ADD CONSTRAINT baz_x_fkey ... FOREIGN KEY`, emitted
AFTER every CREATE TABLE (that is how pg_dump breaks mutual-FK cycles). Pinned
invariant: FK ADD CONSTRAINT (@39048) after BOTH `CREATE TABLE public.bar`
(@16740) and `public.baz` (@16927), AND `REFERENCES public.bar` is ABSENT from
the baz table body (proves FK was split, not inlined). Slices 51/53 only
asserted ADD CONSTRAINT text PRESENCE; this slice pins POSITION + post-data
split, complementing slice 132's view-ordering guard (both dependency-edge
classes now positionally locked).
Files: internal/testport/pgdump_connsetup_test.go (slice-133 assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 133), .ralph/fix_plan.md.
Verified: TestPort_PgDumpConnectionSetup PASS (1.97s).
Committed + pushed.

Next direction (slice 134): a partial-index predicate round-trip
(`CREATE INDEX ... WHERE`), OR a generated-column
(`GENERATED ALWAYS AS ... STORED`) round-trip, OR a UNIQUE NULLS NOT DISTINCT
constraint (NOTE: parser has NO `NULLS NOT DISTINCT` support yet — real
feature, not a guard).
