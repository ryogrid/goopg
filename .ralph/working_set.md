Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 42 COMPLETE
and pushed. NOTHING in flight; next loop starts on slice 43
(add pg_get_function_identity_arguments + sibling catalog functions).

=== DONE (loop #65) — DU-002 slice 42 ===
Made dumpFunc's `pg_proc p, pg_language l WHERE p.oid=$1 AND l.oid=p.prolang`
join resolve. TWO coupled fixes:
(a) Populated pg_language VirtualRows (internal/catalog/catalog.go ~3576) with
    the 3 built-in rows internal/12, c/13, sql/14 (lanvalidator=0, lanacl=NULL,
    all lanispl=f so getProcLangs' WHERE lanispl still returns 0).
(b) Retyped pg_proc view's prolang column text→oid
    (internal/initdb/pg_proc_view.go) to match PG + the physical pg_proc
    catalog. The join was oid=text → silently 0 rows ("0 rows instead of one");
    now oid=oid resolves. Built-in stubs already emitted OID-string langs; user
    routines map name→OID via NEW helper langNameToOIDStr (plpgsql, absent from
    pg_language, → "0"/InvalidOid).
Files: internal/catalog/catalog.go (pg_language rows + comment),
internal/initdb/pg_proc_view.go (prolang oid type + langNameToOIDStr helper +
user-routine row site + doc), internal/initdb/pg_proc_view_test.go
(TestPgProcViewProlangOID NEW; TestPgProcViewRendersRoutine prolang→"0";
TestPgProcViewProsupport unchanged), internal/catalog/catalog_test.go
(TestPgLanguageBuiltinRows NEW), internal/testport/pgdump_connsetup_test.go
(header → next blocker), docs/design/0110-0001-pg-dump-tap-port.md (slice 42).
Gates: gofmt/build clean; catalog pkg PASS; initdb pkg PASS (137s);
TestPort_PgDumpConnectionSetup PASS (advanced past the join).
tpch-spotcheck N/A (catalog-view + column-type change; no executor/codec change).
Verified live: prolang now oid, join returns 'internal', pg_proc scans clean.

=== NEXT STEP — DU-002 slice 43 (pg_get_function_identity_arguments) ===
pg_dump now fails with: `function pg_catalog.pg_get_function_identity_arguments
does not exist` (EXECUTE dumpFunc('1654')). dumpFunc's SELECT projects
pg_get_function_arguments(p.oid), pg_get_function_identity_arguments(p.oid),
pg_get_function_result(p.oid) — goopg implements NONE yet. Add these builtin
catalog functions (start with identity_arguments). Search internal/executor for
existing pg_get_* builtins to mirror the registration pattern. Then RUN
TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): plpgsql user functions can't be
dumped (plpgsql not in pg_language → prolang=0 → dumpFunc join still 0 rows);
reading text[] heap column yields BINARY array encoding (not hit here).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
