(idle — nothing in flight)

Last landed: DU-002 slice 266 (loop #33) — a CHILD-ONLY `NOT NULL` override on
a partition leaf round-trips through pg_dump. This is the LAST of the three
per-column override forms (slice 264 CHECK, slice 265 DEFAULT, 266 NOT NULL).
`pnnl_1` is a LIST leaf of `pnnl (a integer, b integer)` carrying `(b NOT NULL)`
in its PARTITION OF column-override list. pg_dump emits it as the inline
decoration `b integer NOT NULL` inside the leaf's printed column list (NOT a
separate CONSTRAINT clause), then `ATTACH ... FOR VALUES IN (1)`. NOT NULL is
local to the leaf (execCreatePartitionChild → catalog.Column.NotNull). Verified
byte-identical vs real PG 18.3 (probed this loop) AND a fresh goopg server (the
test runs pg_dump against goopg). NO production change.

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — pnnl/pnnl_1 fixture (after the
  pdfl_1 block, ~line 1363) + block-scoped assert on the leaf body
  (`a integer`, `b integer NOT NULL`) and the ATTACH bound (~line 3722).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 266 section + Next note.
- .ralph/fix_plan.md — slice 266 progress entry (loop #33).

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.41s); pgbench pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 267+): the three per-column override forms are now all covered for
a partition leaf. Move to INHERITS-tree dump fidelity beyond the single child —
a local constraint or DEFAULT on a legacy (non-partition) INHERITS child, where
attislocal/conislocal interplay differs (ispartition no longer forces every
column to print, so shouldPrintColumn gates on attislocal only).
