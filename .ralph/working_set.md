Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 47 COMPLETE
(committed this loop). NEXT loop starts on slice 48. NOTHING in flight after commit.

=== DONE (loop #70) — DU-002 slice 47 (array-typed virtual cell → NULL) ===
After slice 46 pg_dump exits 0 with the correct column list, but the dumped
`CREATE TABLE public.foo` still carried a spurious `WITH (""='')` reloptions
clause. Root cause: the virtual pg_class view (internal/catalog/catalog.go)
stores relacl/reloptions as "" (commented "NULL"), but planner.TypedVirtualCell
had NO case for array column types, so the empty cell became StringConst(""),
which goopg's array machinery parses as a single empty-string element ({""}) —
array_length(reloptions,1)=1. pg_dump's getTables reads
`array_remove(array_remove(c.reloptions,…),…)` and nonemptyReloptions (strlen>2)
saw {""} as non-empty → emitted `WITH (` + fmtId("") + `='')`.
Fix: TypedVirtualCell now maps an EMPTY array-typed cell (text[]/_text,
aclitem[]/_aclitem, oid[]/_oid, int2[]/int4[], char[], name[], float4[],
anyarray) to NullConst (PG's convention for absent reloptions / default ACL);
a non-empty cell passes through as the array text literal. Dumped CREATE TABLE
now has no WITH clause — byte-identical to upstream pg_dump for a plain table.
This is the single type-conversion choke point, so it also repairs every other
array-typed virtual-catalog column (proconfig, proacl, nspacl, datacl, …).
NOTE: I first hypothesised the bug was in the PG-physical heap decode path
(decodePhysicalPGValueMctx lacks array cases) and wrote a decoder there — that
path is NOT what serves the SELECT (the virtual pg_class view shadows it), so I
REVERTED codec.go. The decode-path gap is real but latent/unexercised; left for
a future slice if a heap-read array column ever surfaces.
Files: internal/planner/planner.go (TypedVirtualCell array case),
internal/planner/virtual_test.go (TestTypedVirtualCell: empty array→NULL,
non-empty {a,b}→passthrough), internal/testport/pgdump_connsetup_test.go
(no-`WITH (` assertion + header comment), docs/design/0110-0001-pg-dump-tap-port.md
(slice 47 section).
Gates: gofmt/build clean (go build ./... OK); planner PASS; executor PASS;
catalog PASS; TestPort_PgDumpConnectionSetup + PgDump001Basic PASS (exit 0, no
WITH clause); Psql001Basic + PgAmcheck001Basic/AllTables + PLpgSQLViaPsql PASS.
Manual: pg_dump against live goopg emits clean `CREATE TABLE public.foo (id
integer, name text);`. Regress suite: 0 assertion failures; tests a–groupingsets
all passed, then the harness server OOM-killed (signal: killed, NO Go panic) at
the memory-heavy groupingsets and couldn't restart (harness runs server without
the cgroup-cap wrapper) → 23 deferred. Environmental, not a regression.
pgbench CI-parity smoke runs in the pre-commit hook. ralph-state-guard OK.

=== NEXT STEP — DU-002 slice 48 ===
Re-run TestPort_PgDumpConnectionSetup with a richer fixture (table WITH real
reloptions, an index, a NOT NULL / DEFAULT column, maybe a second schema) and
inspect stdout for the next schema-fidelity gap. Known orthogonal pre-existing:
plpgsql user functions can't be dumped (plpgsql absent from pg_language →
prolang=0 → dumpFunc join 0 rows). Promote 002_pg_dump (E-002) toward `port`
once the schema dump round-trips. RUN the TAP test first — it finds the REAL
next blocker.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
