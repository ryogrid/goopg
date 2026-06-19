(idle — nothing in flight)

Last landed: DU-002 slice 264 (loop #31) — a CHILD-ONLY CHECK constraint on a
partition leaf now round-trips through pg_dump. Prior partition fixtures only
exercised bounds + storage/access-method clauses, never a local constraint on a
leaf. `pchk_1` is a LIST leaf of `pchk` carrying `(CONSTRAINT pchk_1_pos CHECK
(a > 0))` in its PARTITION OF column-override list. A partition child prints NO
columns (all inherited), but the named CHECK is LOCAL (IsLocal=true via
tbl.AddCheck), so pg_constraint emits conislocal='t' and pg_get_constraintdef
renders `CHECK ((a > 0))`; real pg_dump 18.3 emits `CONSTRAINT pchk_1_pos CHECK
((a > 0))` inside the column-less CREATE TABLE + the ATTACH. NO production code
changed — column-override CHECK path (M0097-0023) + pg_constraint/
pg_get_constraintdef CHECK branches (slice 49) already existed; proven+guarded.

Files (test/docs only):
- internal/testport/pgdump_connsetup_test.go — pchk/pchk_1 fixture (after the
  psub block) + two assertions (CONSTRAINT pchk_1_pos CHECK ((a > 0)); ATTACH
  PARTITION public.pchk_1 FOR VALUES IN (1)).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 264 section + Next note.
- .ralph/fix_plan.md — slice 264 progress entry.

Gates: gofmt clean; go build ./... clean; TestPort_PgDumpConnectionSetup PASS
(3.35s, byte-matches real pg_dump 18.3); pgbench pre-commit smoke runs on commit.

Next (slice 265+): a child-only DEFAULT or NOT NULL override on a partition leaf
(the other two column-override forms), or a local constraint on a legacy
INHERITS child (inheritance-tree dump fidelity beyond the single child).
